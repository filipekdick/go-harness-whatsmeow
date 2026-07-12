#!/data/data/com.termux/files/usr/bin/bash
# Termux:Boot entry point. Install:
#
#   1. Install the Termux:Boot app (F-Droid, same signing key as Termux)
#      and open it once so Android registers the boot receiver.
#   2. cp scripts/termux-boot.sh ~/.termux/boot/99-harness.sh
#   3. chmod +x ~/.termux/boot/99-harness.sh
#
# On device reboot this runs automatically and hands off to the
# supervising launcher, which holds the wakelock and restarts on crash.

termux-wake-lock
nohup "$HOME/harness/repo/scripts/start.sh" >/dev/null 2>&1 &
