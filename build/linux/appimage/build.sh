#!/usr/bin/env bash
# Copyright (c) 2018-Present Lea Anthony
# SPDX-License-Identifier: MIT

set -euxo pipefail

APP_DIR="${APP_NAME}.AppDir"

if [ -z "${APPIMAGE_RUNTIME_FILE:-}" ]; then
  echo "APPIMAGE_RUNTIME_FILE is required" >&2
  exit 1
fi

if [ ! -s "${APPIMAGE_RUNTIME_FILE}" ]; then
  echo "local AppImage runtime file is missing" >&2
  exit 1
fi

mkdir -p "${APP_DIR}/usr/bin"
cp -r "${APP_BINARY}" "${APP_DIR}/usr/bin/"
cp "${ICON_PATH}" "${APP_DIR}/${APP_NAME}.png"
cp "${ICON_PATH}" "${APP_DIR}/.DirIcon"
cp "${DESKTOP_FILE}" "${APP_DIR}/"

if [[ $(uname -m) == *x86_64* ]]; then
  wget -q -4 -N https://github.com/linuxdeploy/linuxdeploy/releases/download/continuous/linuxdeploy-x86_64.AppImage
  chmod +x linuxdeploy-x86_64.AppImage
  export LDAI_RUNTIME_FILE="${APPIMAGE_RUNTIME_FILE}"
  ./linuxdeploy-x86_64.AppImage --appdir "${APP_DIR}" --output appimage
else
  wget -q -4 -N https://github.com/linuxdeploy/linuxdeploy/releases/download/continuous/linuxdeploy-aarch64.AppImage
  chmod +x linuxdeploy-aarch64.AppImage
  export LDAI_RUNTIME_FILE="${APPIMAGE_RUNTIME_FILE}"
  ./linuxdeploy-aarch64.AppImage --appdir "${APP_DIR}" --output appimage
fi

mkdir -p "${OUTPUT_DIR}"
mv "${APP_NAME}"*.AppImage "${OUTPUT_DIR}/${APP_NAME}.AppImage"
