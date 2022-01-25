# archupd

Arch Linux updater, which does the following:

- Run `sudo pacman -Sc`: clean up old packages
- Run `sudo pacman -Syu`: update outdated packages
- Show relevant pacman logfile contents, which includes the old and new version
  of each package
- Show any new package changelog entries
  - In practice, most packages don't use these, but when they do, it can be
    interesting
- Offer to remove packages that have become unrequired
- Display any new official Arch Linux news from RSS feed
  - Feed contents are retrieved in the background
