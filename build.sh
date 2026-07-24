#!/bin/bash
# build.sh — Compilar XionIA como librería para Android/Desktop
# Uso: ./build.sh [android|linux|windows|macos]

set -e

TARGET=${1:-linux}
OUTPUT_DIR="android/jniLibs"

# Detectar arquitectura
ARCH=$(uname -m)
case $ARCH in
    x86_64)  ANDROID_ARCH="arm64" ; LIB_DIR="arm64-v8a" ;;
    aarch64) ANDROID_ARCH="arm64" ; LIB_DIR="arm64-v8a" ;;
    *)       ANDROID_ARCH="arm"   ; LIB_DIR="armeabi-v7a" ;;
esac

echo "🦾 Jami+++ Build Script"
echo "   Target: $TARGET"
echo "   Arch: $ANDROID_ARCH ($LIB_DIR)"

case $TARGET in
    android)
        echo "📱 Compilando para Android..."
        # Necesitás Android NDK configurado
        export CC=$ANDROID_NDK/toolchains/llvm/prebuilt/linux-x86_64/bin/aarch64-linux-android21-clang
        export CXX=$ANDROID_NDK/toolchains/llvm/prebuilt/linux-x86_64/bin/aarch64-linux-android21-clang++

        mkdir -p "$OUTPUT_DIR/$LIB_DIR"

        GOOS=android GOARCH=$ANDROID_ARCH CGO_ENABLED=1             go build -buildmode=c-shared             -o "$OUTPUT_DIR/$LIB_DIR/libxionia.so"             xionia-lib/mobile.go

        echo "✅ Librería Android: $OUTPUT_DIR/$LIB_DIR/libxionia.so"
        echo "   Copiar a jami-android/app/src/main/jniLibs/$LIB_DIR/"
        ;;

    linux)
        echo "🐧 Compilando para Linux..."
        go build -buildmode=c-shared             -o "libxionia.so"             xionia-lib/mobile.go
        echo "✅ Librería Linux: libxionia.so"
        ;;

    windows)
        echo "🪟 Compilando para Windows..."
        GOOS=windows GOARCH=amd64 CGO_ENABLED=1             CC=x86_64-w64-mingw32-gcc             go build -buildmode=c-shared             -o "xionia.dll"             xionia-lib/mobile.go
        echo "✅ Librería Windows: xionia.dll"
        ;;

    macos)
        echo "🍎 Compilando para macOS..."
        GOOS=darwin GOARCH=amd64 CGO_ENABLED=1             go build -buildmode=c-shared             -o "libxionia.dylib"             xionia-lib/mobile.go
        echo "✅ Librería macOS: libxionia.dylib"
        ;;

    *)
        echo "Uso: ./build.sh [android|linux|windows|macos]"
        exit 1
        ;;
esac

echo ""
echo "🧉 Listo. El albañil puede empezar."
