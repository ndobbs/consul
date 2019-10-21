#!/bin/bash

set -euo pipefail


if [[ ! -f Alice.crt ]] || [[ ! -f Alice.key ]]; then
    echo "Regenerating Alice.{crt,key}..."
    rm -f Alice.crt Alice.key
    openssl req -new -sha256 -nodes -out Alice.csr -newkey rsa:2048 -keyout Alice.key -config Alice.cfg
    openssl ca -batch -config myca.conf -extfile Alice.ext -notext -in Alice.csr -out Alice.crt
    rm -f Alice.csr
fi

if [[ ! -f Bob.crt ]] || [[ ! -f Bob.key ]]; then
    echo "Regenerating Bob.{crt,key}..."
    rm -f Bob.crt Bob.key
    openssl req -new -sha256 -nodes -out Bob.csr -newkey rsa:2048 -keyout Bob.key -config Bob.cfg
    openssl ca -batch -config myca.conf -extfile Bob.ext -notext -in Bob.csr -out Bob.crt
    rm -f Bob.csr
fi
