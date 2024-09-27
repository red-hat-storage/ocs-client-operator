#!/bin/bash

git diff --quiet -I'^( )+createdAt: ' bundle
if ((! $?)) ; then
    git checkout --quiet bundle
fi
