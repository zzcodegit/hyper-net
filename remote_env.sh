#!/bin/sh
# Источник этого файла на сервере перед ручным запуском go test / go build:
#   source remote_env.sh   или   . ./remote_env.sh
export PATH="/usr/local/go/bin:$PATH"
export GOTOOLCHAIN=local
