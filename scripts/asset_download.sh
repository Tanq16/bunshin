#!/bin/bash

# Asset download script for Bunshin
# Downloads all external dependencies locally

set -e

CSS_DIR="frontend/static/css"
JS_DIR="frontend/static/js"
FONTS_DIR="frontend/static/fonts"

echo "Creating directory structure"
mkdir -p "$CSS_DIR" "$JS_DIR" "$FONTS_DIR"

echo "Downloading Tailwind CSS"
curl -sL "https://cdn.tailwindcss.com" -o "$JS_DIR/tailwindcss.js"

echo "Downloading Font Awesome"
curl -sL "https://unpkg.com/@fortawesome/fontawesome-free@7.1.0/css/all.min.css" -o "$CSS_DIR/fontawesome.min.css"

echo "Downloading Font Awesome fonts"
grep -oE "url\([^)]*webfonts/[^)]*\)" "$CSS_DIR/fontawesome.min.css" | \
    sed "s|url(||g" | sed "s|)||g" | sed "s|['\"]||g" | \
    while read -r font_path; do
        filename=$(basename "$font_path")
        echo "  $filename"
        curl -sL "https://unpkg.com/@fortawesome/fontawesome-free@7.1.0/webfonts/$filename" -o "$FONTS_DIR/$filename"
    done

echo "Updating Font Awesome CSS paths"
sed -i.bak 's|url(../webfonts/|url(/static/fonts/|g; s|url(/webfonts/|url(/static/fonts/|g' "$CSS_DIR/fontawesome.min.css"
rm -f "$CSS_DIR/fontawesome.min.css.bak"

echo "Downloading Highlight.js"
curl -sL "https://cdnjs.cloudflare.com/ajax/libs/highlight.js/11.11.1/highlight.min.js" -o "$JS_DIR/highlight.min.js"
curl -sL "https://cdnjs.cloudflare.com/ajax/libs/highlight.js/11.11.1/languages/yaml.min.js" -o "$JS_DIR/highlight-yaml.min.js"
curl -sL "https://cdnjs.cloudflare.com/ajax/libs/highlight.js/11.11.1/languages/ini.min.js" -o "$JS_DIR/highlight-ini.min.js"

echo "Downloading Catppuccin theme"
curl -sL "https://unpkg.com/@catppuccin/highlightjs@1.0.1/css/catppuccin-mocha.css" -o "$CSS_DIR/catppuccin-mocha.css"

echo "Downloading Xterm.js"
curl -sL "https://cdn.jsdelivr.net/npm/xterm@5.3.0/css/xterm.css" -o "$CSS_DIR/xterm.css"
curl -sL "https://cdn.jsdelivr.net/npm/xterm@5.3.0/lib/xterm.min.js" -o "$JS_DIR/xterm.min.js"

echo "Downloading ansi_up"
curl -sL "https://cdn.jsdelivr.net/npm/ansi_up@5.1.0/ansi_up.js" -o "$JS_DIR/ansi_up.js"

echo "Downloading Google Fonts"
curl -sL "https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700&display=swap" -A "Mozilla/5.0" -o "$CSS_DIR/inter.css"
curl -sL "https://fonts.googleapis.com/css2?family=JetBrains+Mono:wght@400;500&display=swap" -A "Mozilla/5.0" -o "$CSS_DIR/jetbrains-mono.css"

echo "Downloading font files"
grep -o "https://fonts.gstatic.com/s/inter/[^)]*" "$CSS_DIR/inter.css" | while read -r url; do
    echo "  Inter: $(basename "$url")"
    curl -sL "$url" -o "$FONTS_DIR/$(basename "$url")"
done

grep -o "https://fonts.gstatic.com/s/jetbrainsmono/[^)]*" "$CSS_DIR/jetbrains-mono.css" | while read -r url; do
    echo "  JetBrains Mono: $(basename "$url")"
    curl -sL "$url" -o "$FONTS_DIR/$(basename "$url")"
done

echo "Updating font CSS paths"
sed -i.bak 's|https://fonts.gstatic.com/s/inter/v[0-9]*/|/static/fonts/|g' "$CSS_DIR/inter.css"
sed -i.bak 's|https://fonts.gstatic.com/s/jetbrainsmono/v[0-9]*/|/static/fonts/|g' "$CSS_DIR/jetbrains-mono.css"
rm -f "$CSS_DIR"/*.bak

echo "Combining font CSS files"
cat "$CSS_DIR/inter.css" "$CSS_DIR/jetbrains-mono.css" > "$CSS_DIR/fonts.css"
rm -f "$CSS_DIR/inter.css" "$CSS_DIR/jetbrains-mono.css"

echo "Done"
