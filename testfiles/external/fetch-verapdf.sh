#!/usr/bin/env bash
# Copyright (c) 2016-2026 by Richard A. Wilkes. All rights reserved.
#
# This Source Code Form is subject to the terms of the Mozilla Public License,
# version 2.0. If a copy of the MPL was not distributed with this file, You
# can obtain one at http://mozilla.org/MPL/2.0/.
#
# Fetches the veraPDF test corpus for the local M8 soak (see plan.md and soak_test.go). The download is
# tag-pinned and checksum-verified; the extracted files land in testfiles/external/veraPDF-corpus, which is
# GITIGNORED — the corpus is never committed to this repository (decision log 2026-07-11).
#
# Attribution: the veraPDF corpus is (c) the veraPDF Consortium, licensed under the Creative Commons
# Attribution 4.0 International license (CC BY 4.0, https://creativecommons.org/licenses/by/4.0/).
# Source: https://github.com/veraPDF/veraPDF-corpus
set -eo pipefail

TAG="v1.28.1"
SHA256="a3029e79e56b3e64a1ead858692a92c02b7327bb857d461ba8e88eb498aba8d1"
URL="https://github.com/veraPDF/veraPDF-corpus/archive/refs/tags/${TAG}.tar.gz"

cd "$(dirname "$0")"
DEST="veraPDF-corpus"
if [ -d "$DEST" ]; then
    echo "$PWD/$DEST already exists; delete it first to re-fetch."
    exit 0
fi

ARCHIVE="verapdf-corpus-${TAG}.tar.gz"
echo "Fetching veraPDF corpus ${TAG}..."
curl -fsSL -o "$ARCHIVE" "$URL"
echo "${SHA256}  ${ARCHIVE}" | shasum -a 256 -c - || {
    echo "Checksum mismatch — refusing to use the download." >&2
    rm -f "$ARCHIVE"
    exit 1
}
mkdir "$DEST"
tar -xzf "$ARCHIVE" -C "$DEST" --strip-components 1
rm -f "$ARCHIVE"
COUNT=$(find "$DEST" -iname '*.pdf' | wc -l | tr -d ' ')
echo "Extracted ${COUNT} PDFs into $PWD/$DEST"
