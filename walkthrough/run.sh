#!/bin/sh
set -eu
# Run in the disposable acceptance browser image. This never bypasses TLS checks.
export HOME=/tmp/mailbox-walkthrough-home
mkdir -p "$HOME/.pki/nssdb"
certutil -N --empty-password -d "sql:$HOME/.pki/nssdb"
node /check/trust.mjs
certutil -A -n mailbox-demo -t 'C,,' -i /inputs/ca.pem -d "sql:$HOME/.pki/nssdb"
node /check/walkthrough.mjs
