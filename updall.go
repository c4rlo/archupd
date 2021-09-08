package main

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const PACMAN_LOGFILE_PATH = "/var/log/pacman.log"
const NEWSFEED_URL = "https://archlinux.org/feeds/news/"

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

type State struct {
	LastModified   string    `json:"last_modified"`
	LatestItemTime time.Time `json:"latest_seen"`
}

func stateFileName() string {
	homePath, err := os.UserHomeDir()
	if err != nil {
		panic(err)
	}
	return filepath.Join(homePath, ".updall-state.json")
}

func readState() State {
	f, err := os.Open(stateFileName())
	if err != nil {
		fmt.Println(err)
		return State{} // ignore err
	}
	defer f.Close()
	var state State
	json.NewDecoder(f).Decode(&state)
	return state
}

func writeState(state *State) {
	f, err := os.Create(stateFileName())
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
		ch <- "Failed to formulate request: " + err.Error()
		return
	}
	if state.LastModified != "" {
		req.Header.Add("If-Modified-Since", state.LastModified)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		ch <- "Failed to send request: " + err.Error()
		return
	}
	if resp.StatusCode == http.StatusNotModified {
		ch <- "No news (HTTP 304)"
		return
	}
	if resp.StatusCode != http.StatusOK {
		ch <- "Unexpected HTTP status: " + resp.Status
		return
	}

	if lastMod := resp.Header.Values("Last-Modified"); lastMod != nil {
		state.LastModified = lastMod[0]
	}

	defer writeState(&state)

	decoder := xml.NewDecoder(resp.Body)
	var feed Feed
	if err = decoder.Decode(&feed); err != nil {
		ch <- err.Error()
		return
	}
	items := feed.Items
	sort.Slice(items, func(i, j int) bool {
		return items[i].Time.After(items[j].Time.Time)
	})

	if len(items) == 0 {
		ch <- "No news (empty RSS feed)"
		return
	}

	gotAny := false
	for _, item := range items {
		if item.Time.After(state.LatestItemTime) {
			ch <- fmt.Sprintf("%s: %s (%s)",
				item.Time.Local().Format("2006-01-02 15:04"), item.Title, item.Link)
			gotAny = true
		}
	}

	state.LatestItemTime = items[0].Time.Time

	if !gotAny {
		ch <- "No new news (HTTP 200)"
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
				fmt.Println("No superfluous packages.")
				return nil
			}
		}
		return err
	}
	pkgs := strings.Split(strings.TrimRight(output.String(), "\n"), "\n")
	if len(pkgs) == 0 {
		return nil
	}

	fmt.Println("Superfluous packages can be removed:")
	args := []string{"pacman", "-Rs"}
	args = append(args, pkgs...)
	cmd = exec.Command("sudo", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
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

func (m *logMonitor) catchUp() (string, error) {
	b, err := io.ReadAll(m)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func exitOnError(err error) {
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func main() {
	newsCh := make(chan string, 10)
	go readNews(newsCh)

	err := pacman("-Sc", "--noconfirm")
	exitOnError(err)

	logMon, err := newLogMonitor(PACMAN_LOGFILE_PATH)
	exitOnError(err)

	err = pacman("-Syu", "--noconfirm")
	exitOnError(err)

	logs, err := logMon.catchUp()
	exitOnError(err)
	fmt.Print(logs)

	err = removeSuperfluousPackages()
	exitOnError(err)

	fmt.Println("\nArch Linux news:")
	for s := range newsCh {
		fmt.Printf("  - %s\n", s)
	}
}
