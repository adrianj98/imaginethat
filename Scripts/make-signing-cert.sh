#!/bin/bash
# Create the self-signed "ImageThat Local" code-signing certificate in your
# login keychain, so every build of the Mac app has the same signature and the
# keychain stops asking for your password after Touch ID (see the Makefile's
# SIGN_IDENTITY). Run it once:
#
#   Scripts/make-signing-cert.sh
#
# macOS asks for your login password once, to trust the certificate for code
# signing. Nothing leaves this Mac; the certificate is only good for signing
# your own builds.
set -euo pipefail

NAME="ImageThat Local"
KEYCHAIN="$HOME/Library/Keychains/login.keychain-db"
# LibreSSL's PKCS#12 output is what `security import` reads; OpenSSL 3+
# defaults to ciphers it rejects.
OPENSSL=/usr/bin/openssl

if security find-identity -v -p codesigning "$KEYCHAIN" 2>/dev/null | grep -q "\"$NAME\""; then
    echo "\"$NAME\" already exists in your login keychain; nothing to do."
    exit 0
fi

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
cd "$WORK"

cat > cert.cnf <<EOF
[req]
distinguished_name = dn
x509_extensions = ext
prompt = no
[dn]
CN = $NAME
[ext]
basicConstraints = critical, CA:false
keyUsage = critical, digitalSignature
extendedKeyUsage = critical, codeSigning
EOF

echo "==> Creating a code-signing certificate \"$NAME\""
"$OPENSSL" req -x509 -newkey rsa:2048 -nodes -days 3650 -sha256 \
    -config cert.cnf -keyout key.pem -out cert.pem 2>/dev/null
# The .p12 only carries the key into the keychain and is deleted on exit, so
# its password is a throwaway.
P12_PASS=$("$OPENSSL" rand -hex 16)
"$OPENSSL" pkcs12 -export -name "$NAME" -inkey key.pem -in cert.pem \
    -out identity.p12 -passout "pass:$P12_PASS"

echo "==> Adding it to your login keychain"
# -T lets codesign use the key without asking each build.
security import identity.p12 -k "$KEYCHAIN" -P "$P12_PASS" -T /usr/bin/codesign >/dev/null

echo "==> Trusting it for code signing (macOS asks for your login password)"
security add-trusted-cert -r trustRoot -p codeSign -k "$KEYCHAIN" cert.pem

if security find-identity -v -p codesigning "$KEYCHAIN" | grep -q "\"$NAME\""; then
    echo "Done. Rebuild with Scripts/install-mac.sh; the next Touch ID unlock asks"
    echo "once more, choose Always Allow, and it stays allowed across builds."
else
    echo "error: \"$NAME\" was imported but is not a valid signing identity" >&2
    exit 1
fi
