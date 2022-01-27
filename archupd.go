package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/pkg/diff"
	diff_write "github.com/pkg/diff/write"
)

const NEWSFEED_URL = "https://archlinux.org/feeds/news/"
const PACMAN_LOG_PATH = "/var/log/pacman.log"

const HELP_STR = `
  Arch Linux updater. Run without args and it will:

  - Run "sudo pacman -Sc" to clean up old packages.
  - Run "sudo pacman -Syu" to update outdated packages.
  - Show relevant pacman logfile contents, which includes the old and new version of each package.
  - Show any new package changelog entries.
  - Offer to remove packages that have become unrequired.
  - Display any new official Arch Linux news from RSS feed.
`

var PACMAN_LOG_ALPM_MARKER = []byte(" [ALPM] ")
var CHANGELOG_PACKAGE_REGEXP = regexp.MustCompile(`^Changelog for (.+):$`)

var helpFlag = false

func init() {
	const helpUsage = "show help"
	flag.BoolVar(&helpFlag, "h", false, helpUsage)
	flag.BoolVar(&helpFlag, "?", false, helpUsage)
	flag.BoolVar(&helpFlag, "help", false, helpUsage)

	flag.Usage = showHelp
}

type Feed struct {
	XMLName xml.Name   `xml:"rss"`
	Items   []FeedItem `xml:"channel>item"`
}

type FeedItem struct {
	Title string  `xml:"title"`
	Link  string  `xml:"link"`
	Time  RSSTime `xml:"pubDate"`
	GUID  string  `xml:"guid"`
}

type RSSTime struct {
	time.Time
}

func (t *RSSTime) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	var s string
	if err := d.DecodeElement(&s, &start); err != nil {
		return err
	}
	var err error
	if t.Time, err = time.Parse(time.RFC1123Z, s); err != nil {
		return err
	}
	return nil
}

func showHelp() {
	fmt.Printf("Usage: %s\n%s", os.Args[0], HELP_STR)
}

type State struct {
	LastModified   string    `json:"last_modified"`
	LatestItemTime time.Time `json:"latest_seen"`
}

func stateFileName() string {
	stateHome := os.Getenv("XDG_STATE_HOME")
	if stateHome == "" {
		homePath, err := os.UserHomeDir()
		if err != nil {
			panic(err)
		}
		stateHome = filepath.Join(homePath, ".local", "state")
	}
	return filepath.Join(stateHome, "archupd.json")
}

func readState() State {
	f, err := os.Open(stateFileName())
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			fmt.Println(err)
		}
		return State{} // ignore err
	}
	defer f.Close()
	var state State
	json.NewDecoder(f).Decode(&state)
	return state
}

func writeState(state *State) {
	fileName := stateFileName()
	if err := os.MkdirAll(filepath.Dir(fileName), 0755); err != nil {
		fmt.Println(err)
		return
	}
	f, err := os.Create(fileName)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer f.Close()
	json.NewEncoder(f).Encode(state)
}

func readNews(ch chan<- string) {
	defer close(ch)

	state := readState()

	req, err := http.NewRequest(http.MethodGet, NEWSFEED_URL, nil)
	if err != nil {
		ch <- "Arch Linux news: failed to formulate request: " + err.Error()
		return
	}
	if state.LastModified != "" {
		req.Header.Add("If-Modified-Since", state.LastModified)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		ch <- "Arch Linux news: failed to send request: " + err.Error()
		return
	}
	if resp.StatusCode == http.StatusNotModified {
		ch <- "No Arch Linux news."
		return
	}
	if resp.StatusCode != http.StatusOK {
		ch <- "Arch Linux news: unexpected HTTP status: " + resp.Status
		return
	}

	if lastMod := resp.Header.Values("Last-Modified"); lastMod != nil {
		state.LastModified = lastMod[0]
	}

	decoder := xml.NewDecoder(resp.Body)
	var feed Feed
	if err = decoder.Decode(&feed); err != nil {
		ch <- "Arch Linux news: failed to decode feed: " + err.Error()
		return
	}

	defer writeState(&state)

	items := feed.Items
	sort.Slice(items, func(i, j int) bool {
		return items[i].Time.After(items[j].Time.Time)
	})

	if len(items) == 0 {
		ch <- "No Arch Linux news (empty feed)."
		return
	}

	foundAny := false
	for _, item := range items {
		if item.Time.After(state.LatestItemTime) {
			if !foundAny {
				ch <- "Arch Linux news:"
			}
			ch <- fmt.Sprintf("  - %s: %s (%s)",
				item.Time.Local().Format("2006-01-02 15:04"), item.Title, item.Link)
			foundAny = true
		}
	}

	state.LatestItemTime = items[0].Time.Time

	if !foundAny {
		ch <- "No Arch Linux news."
	}
}

func pacman(args ...string) error {
	cmdArgs := append([]string{"pacman"}, args...)
	cmd := exec.Command("sudo", cmdArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func removeSuperfluousPackages() error {
	var output strings.Builder
	cmd := exec.Command("sudo", "pacman", "-Qqtd")
	cmd.Stdout = &output
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if err != nil {
		if err, ok := err.(*exec.ExitError); ok {
			if err.ExitCode() == 1 {
				fmt.Println("\nNo superfluous packages.")
				return nil
			}
		}
		return err
	}
	pkgs := strings.Split(strings.TrimRight(output.String(), "\n"), "\n")
	if len(pkgs) == 0 {
		return nil
	}

	fmt.Println("\nSuperfluous packages can be removed:")
	args := []string{"pacman", "-Rs"}
	args = append(args, pkgs...)
	cmd = exec.Command("sudo", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func getChangelogs() (map[string]string, error) {
	cmd := exec.Command("pacman", "-Qc")
	outputReader, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	result := make(map[string]string)
	var currPkg string
	var currLog strings.Builder
	scanner := bufio.NewScanner(outputReader)
	for scanner.Scan() {
		line := scanner.Bytes()
		if matches := CHANGELOG_PACKAGE_REGEXP.FindSubmatch(line); matches != nil {
			if currPkg != "" {
				result[currPkg] = currLog.String()
			}
			currLog.Reset()
			currPkg = string(matches[1])
		} else {
			currLog.Write(line)
			currLog.WriteByte('\n')
		}
	}
	if currPkg != "" {
		result[currPkg] = currLog.String()
	}
	if err := cmd.Wait(); err != nil {
		return nil, err
	}
	return result, nil
}

type logMonitor struct {
	*os.File
}

func newLogMonitor(path string) (*logMonitor, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	_, err = f.Seek(0, os.SEEK_END)
	if err != nil {
		return nil, err
	}
	return &logMonitor{f}, nil
}

func (m *logMonitor) lines() *bufio.Scanner {
	return bufio.NewScanner(m)
}

func showChangelogDiff(changelogsPre, changelogsPost map[string]string) {
	foundAny := false
	for pkg, logPost := range changelogsPost {
		if logPre, ok := changelogsPre[pkg]; ok && logPre != logPost {
			if !foundAny {
				fmt.Println("\nChangelog diffs:\n")
			}
			err := diff.Text(
				pkg+" (before)",
				pkg+" (after)",
				logPre,
				logPost,
				os.Stdout,
				diff_write.TerminalColor(),
			)
			foundAny = true
			if err != nil {
				fmt.Println(err)
			}
		}
	}
	if !foundAny {
		fmt.Println("\nNo updated changelogs.")
	}
}

func exitOnError(err error) {
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func main() {
	flag.Parse()
	if helpFlag {
		showHelp()
		return
	}

	newsCh := make(chan string, 10)
	go readNews(newsCh)

	err := pacman("-Sc", "--noconfirm")
	exitOnError(err)

	changelogsPre, err := getChangelogs()
	exitOnError(err)

	logMon, err := newLogMonitor(PACMAN_LOG_PATH)
	exitOnError(err)

	err = pacman("-Syu", "--noconfirm")
	exitOnError(err)

	lines := logMon.lines()
	foundALPMLogs := false
	for lines.Scan() {
		line := lines.Bytes()
		if bytes.Contains(line, PACMAN_LOG_ALPM_MARKER) {
			if !foundALPMLogs {
				fmt.Println("\nALPM logs:")
				foundALPMLogs = true
			}
			fmt.Printf("%s\n", line)
		}
	}

	if foundALPMLogs {
		changelogsPost, err := getChangelogs()
		exitOnError(err)

		showChangelogDiff(changelogsPre, changelogsPost)

		err = removeSuperfluousPackages()
		exitOnError(err)
	}

	fmt.Println()
	for s := range newsCh {
		fmt.Println(s)
	}
}
