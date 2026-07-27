#!/bin/bash
# Compila libxionia.so para Android (arm64-v8a, armeabi-v7a, x86_64)
# y la copia directo a la carpeta de jniLibs de Flutter.
#
# Requiere: Android NDK instalado y la variable ANDROID_NDK_HOME apuntando
# a esa instalación (ej: export ANDROID_NDK_HOME=$HOME/Android/Sdk/ndk/26.3.11579264)
#
# Uso: ./build_android.sh   (correr desde la raíz de xionia-xtp)

set -e

if [ -z "$ANDROID_NDK_HOME" ]; then
  echo "ERROR: definí ANDROID_NDK_HOME antes de correr este script."
  echo "Ejemplo: export ANDROID_NDK_HOME=\$HOME/Android/Sdk/ndk/26.3.11579264"
  exit 1
fi

TOOLCHAIN="$ANDROID_NDK_HOME/toolchains/llvm/prebuilt/linux-x86_64/bin"
API=24
OUT_BASE="xionchat_flutter/android/app/src/main/jniLibs"

mkdir -p "$OUT_BASE/arm64-v8a" "$OUT_BASE/armeabi-v7a" "$OUT_BASE/x86_64"

echo "== arm64-v8a =="
CGO_ENABLED=1 GOOS=android GOARCH=arm64 \
  CC="$TOOLCHAIN/aarch64-linux-android${API}-clang" \
  go build -buildmode=c-shared -o "$OUT_BASE/arm64-v8a/libxionia.so" mobile.go

echo "== armeabi-v7a =="
CGO_ENABLED=1 GOOS=android GOARCH=arm GOARM=7 \
  CC="$TOOLCHAIN/armv7a-linux-androideabi${API}-clang" \
  go build -buildmode=c-shared -o "$OUT_BASE/armeabi-v7a/libxionia.so" mobile.go

echo "== x86_64 (para el emulador) =="
CGO_ENABLED=1 GOOS=android GOARCH=amd64 \
  CC="$TOOLCHAIN/x86_64-linux-android${API}-clang" \
  go build -buildmode=c-shared -o "$OUT_BASE/x86_64/libxionia.so" mobile.go

echo ""
echo "Listo. .so generadas en:"
find "$OUT_BASE" -name "libxionia.so"
