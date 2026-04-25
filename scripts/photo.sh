#!/usr/bin/env bash
set -euo pipefail

node cashier/photo.mjs $1 $2
node cashier/cli.mjs build $2 > $3

open $3
