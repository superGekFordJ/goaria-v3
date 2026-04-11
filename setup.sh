#!/bin/bash
set -e

# setup.sh - Linux setup script for GoAria v3
# This script mimics the logic of setup.ps1 for Linux environments.

echo "🚀 GoAria v3 Development Setup (Linux)..."

# -------------------------------------------------------------------------
# 1. Check pnpm
# -------------------------------------------------------------------------
echo -n "Checking pnpm... "
if command -v pnpm &> /dev/null; then
    echo "✅ [OK]"
else
    echo "❌ [MISSING]"
    echo "Please install pnpm first (e.g. corepack prepare pnpm@latest --activate)"
    exit 1
fi

# -------------------------------------------------------------------------
# 2. Install System Dependencies & Aria2
# -------------------------------------------------------------------------
echo "📦 Checking System Dependencies..."

# Check for apt-get (Debian/Ubuntu)
if command -v apt-get &> /dev/null; then
    PKGS="libgtk-3-dev libwebkit2gtk-4.1-dev aria2"
    MISSING_PKGS=""

    # Check if packages are installed (simple dpkg check)
    for pkg in $PKGS; do
        if ! dpkg -s $pkg &> /dev/null; then
            MISSING_PKGS="$MISSING_PKGS $pkg"
        fi
    done

    if [ -n "$MISSING_PKGS" ]; then
        echo "Installing missing dependencies: $MISSING_PKGS"
        if [ "$EUID" -ne 0 ]; then
            sudo apt-get update
            sudo apt-get install -y $MISSING_PKGS
        else
            apt-get update
            apt-get install -y $MISSING_PKGS
        fi
    else
        echo "✅ System libraries (GTK3, WebKit2GTK-4.1, Aria2) appear installed."
    fi
else
    # Fallback/Other distros checks
    if ! pkg-config --exists gtk+-3.0 webkit2gtk-4.1 2>/dev/null; then
        echo "⚠️  Missing GTK3 or WebKit2GTK-4.1 development headers."
        echo "   Please manually install them (e.g., libgtk-3-dev, libwebkit2gtk-4.1-dev)."
    else
        echo "✅ GTK3 and WebKit2GTK detected via pkg-config."
    fi

    if ! command -v aria2c &> /dev/null; then
        echo "⚠️  aria2c not found. Please install it."
    fi
fi

# -------------------------------------------------------------------------
# 3. Check & Install Wails 3
# -------------------------------------------------------------------------
echo -n "Checking wails3... "
if command -v wails3 &> /dev/null; then
    echo "✅ [OK]"
else
    echo "⚠️  [MISSING] Installing Wails 3 CLI..."

    # Try installing
    if go install github.com/wailsapp/wails/v3/cmd/wails3@latest; then
         # Ensure GOPATH/bin is in PATH
        export PATH=$PATH:$(go env GOPATH)/bin
        echo "✅ Wails 3 installed successfully."
    else
        echo "❌ Failed to install Wails 3."
        echo "   This is often due to missing C library headers (libgtk-3-dev, libwebkit2gtk-4.1-dev)."
        exit 1
    fi
fi

# -------------------------------------------------------------------------
# 4. Prepare Aria2 Binary (Bundled Runtime Input)
# -------------------------------------------------------------------------
# Linux builds embed the staged binary from internal/process/bundled/linux/aria2c.

SCRIPT_DIR=$(dirname "$(realpath "$0")")
TARGET_DIR="$SCRIPT_DIR/internal/process/bundled/linux"
TARGET_FILE="$TARGET_DIR/aria2c"
ARIA2_BIN=$(command -v aria2c || true)

if [ -f "$ARIA2_BIN" ]; then
    echo "⬇️  Copying system aria2c ($ARIA2_BIN) to internal/process/bundled/linux/aria2c..."
    mkdir -p "$TARGET_DIR"
    cp "$ARIA2_BIN" "$TARGET_FILE"
    chmod +x "$TARGET_FILE"
    echo "✅ Binary ready for embedding."
else
    echo "❌ Error: aria2c binary not found even after attempted install."
    exit 1
fi

echo ""
echo "🎉 Setup Complete!"
echo "To run tests:wails3 task test:all"
echo "To run setup for docker:wails3 task setup:docker"
