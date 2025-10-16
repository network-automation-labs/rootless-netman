#!/bin/sh
set -e

systemctl stop rootless-netman.socket rootless-netman.service
systemctl disable rootless-netman.socket rootless-netman.service

exit 0
