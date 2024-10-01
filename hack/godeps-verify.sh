#!/usr/bin/env bash

paths=('go.mod' 'go.sum' 'vendor/' 'api/go.mod' 'api/go.sum' 'api/vendor/')

if [[ -n "$(git status --porcelain "${paths[@]}")" ]]; then
	git diff -u "${paths[@]}"
	echo "Inconsistency found in dependency files. Run 'make godeps-update' and commit results."
	exit 1
fi
echo "Success: no out of source tree changes found for dependency files"
