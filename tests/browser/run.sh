#!/bin/sh
set -eu
# Trust only this lab's CA in a disposable browser home. Never bypass TLS errors.
export HOME=/tmp/mailbox-browser-home
mkdir -p "$HOME/.pki/nssdb"
certutil -N --empty-password -d "sql:$HOME/.pki/nssdb"
node /check/trust.mjs
certutil -A -n mailbox-lab -t 'C,,' -i /inputs/ca.pem -d "sql:$HOME/.pki/nssdb"
node /check/check.mjs
