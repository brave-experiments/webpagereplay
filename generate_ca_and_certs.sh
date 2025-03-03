#!/bin/bash
set -euo pipefail

# Script to generate a local CA and server SSL certificate valid for multiple SANs

# --- Configuration Variables ---
OUTPUT_DIR="cert"
CA_KEY="$OUTPUT_DIR/myCA.key"
CA_CERT="$OUTPUT_DIR/myCA.crt"
SERVER_CSR="$OUTPUT_DIR/server.csr"
HASH_FILE="$OUTPUT_DIR/hash.txt"
SERVER_CERT="$OUTPUT_DIR/cert.pem"      # Server certificate file
SERVER_KEY_PEM="$OUTPUT_DIR/key.pem"      # Server private key file

CA_DAYS="3650"      # CA certificate validity (days)
SERVER_DAYS="365"   # Server certificate validity (days)
KEY_SIZE="2048"     # Key size in bits
DIGEST="sha256"     # Digest algorithm

# --- Default Certificate Information ---
DEFAULT_COUNTRY="US"
DEFAULT_STATE="California"
DEFAULT_LOCALITY="San Francisco"
DEFAULT_ORGANIZATION="My Organization"
DEFAULT_UNIT="IT Department"
DEFAULT_COMMON_NAME="localhost"  # Common Name is localhost
DEFAULT_EMAIL="admin@example.com"

# Build the certificate subject string
SUBJECT="/C=$DEFAULT_COUNTRY/ST=$DEFAULT_STATE/L=$DEFAULT_LOCALITY/O=$DEFAULT_ORGANIZATION/OU=$DEFAULT_UNIT/CN=$DEFAULT_COMMON_NAME/emailAddress=$DEFAULT_EMAIL"

# --- Define Host IP and Alternate Domains ---
HOST_IP="192.168.1.190"
ALT_DOMAINS=("localhost" "*.amazon.co.uk" "*.cnn.com" "*.wikipedia.org" "*.globo.com" "*.google.com")

# Generate SAN_ENTRIES array from HOST_IP and ALT_DOMAINS.
# We want "DNS:localhost" first, then "IP:192.168.1.190", then the rest.
SAN_ENTRIES=("DNS:${ALT_DOMAINS[0]}" "IP:${HOST_IP}")
for domain in "${ALT_DOMAINS[@]:1}"; do
  SAN_ENTRIES+=("DNS:${domain}")
done

# --- Dependency Check ---
if ! command -v openssl >/dev/null 2>&1; then
  echo "Error: OpenSSL is not installed." >&2
  exit 1
fi

# --- Function Definitions ---

# Generate an RSA key
generate_key() {
  local output_file="$1"
  local key_size="${2:-$KEY_SIZE}"
  openssl genrsa -out "$output_file" "$key_size"
  echo "Generated key: $output_file"
}

# Generate a self-signed CA certificate
generate_ca_cert() {
  local key_file="$1"
  local cert_file="$2"
  local days="${3:-$CA_DAYS}"
  local digest="${4:-$DIGEST}"

  openssl req -x509 -new -nodes -key "$key_file" -$digest -days "$days" -out "$cert_file" -subj "$SUBJECT"
  echo "Generated CA certificate: $cert_file"
}

# Generate a Certificate Signing Request (CSR)
generate_csr() {
  local key_file="$1"
  local csr_file="$2"
  local digest="${3:-$DIGEST}"

  openssl req -new -key "$key_file" -out "$csr_file" -$digest -subj "$SUBJECT"
  echo "Generated CSR: $csr_file"
}

# Sign a CSR with the CA's key and certificate to generate a server certificate.
# The certificate will include SAN entries as specified in the global SAN_ENTRIES array.
sign_csr() {
  local csr_file="$1"
  local ca_cert="$2"
  local ca_key="$3"
  local cert_file="$4"
  local days="${5:-$SERVER_DAYS}"
  local digest="${6:-$DIGEST}"

  # Join SAN entries into a comma-separated list
  local SAN
  SAN=$(IFS=,; echo "${SAN_ENTRIES[*]}")

  openssl x509 -req -in "$csr_file" \
    -CA "$ca_cert" -CAkey "$ca_key" -CAcreateserial \
    -out "$cert_file" -days "$days" -$digest \
    -extfile <(printf "subjectAltName=${SAN}")
  echo "Generated server certificate: $cert_file"
}

# Generate a hash from the server certificate public key
gen_hash() {
  local cert_file="$1"
  local hash_file="$2"
  openssl x509 -noout -pubkey -in "$cert_file" | \
    openssl pkey -pubin -outform der | \
    openssl dgst -sha256 -binary | \
    base64 > "$hash_file"
}

# --- Main Script ---

# Remove the output directory if it exists (with prompt)
if [ -d "$OUTPUT_DIR" ]; then
  read -r -p "Output directory '$OUTPUT_DIR' already exists. Remove it? (y/n): " response
  case "$response" in
    [Yy][Ee][Ss]|[Yy])
      rm -rf "$OUTPUT_DIR"
      echo "Removed existing output directory."
      ;;
    *)
      echo "Exiting without changes."
      exit 1
      ;;
  esac
fi

# Create output directory
mkdir -p "$OUTPUT_DIR"
echo "Created output directory: $OUTPUT_DIR"

# Generate CA key and certificate
echo "--- Generating CA key ---"
generate_key "$CA_KEY"

echo "--- Generating CA certificate ---"
generate_ca_cert "$CA_KEY" "$CA_CERT" "$CA_DAYS" "$DIGEST"

# Generate server key and CSR
echo "--- Generating server key ---"
generate_key "$SERVER_KEY_PEM"

echo "--- Generating server CSR ---"
generate_csr "$SERVER_KEY_PEM" "$SERVER_CSR" "$DIGEST"

# Sign server CSR with the CA to create the server certificate (with SAN extension)
echo "--- Signing server CSR with CA ---"
sign_csr "$SERVER_CSR" "$CA_CERT" "$CA_KEY" "$SERVER_CERT" "$SERVER_DAYS" "$DIGEST"

# Generate hash from the server certificate
echo "--- Generating certificate hash ---"
gen_hash "$SERVER_CERT" "$HASH_FILE"

# Display summary of generated files
echo "--- Done ---"
echo "Files generated in: $OUTPUT_DIR"
echo "  - CA Key:        $(basename "$CA_KEY")"
echo "  - CA Certificate: $(basename "$CA_CERT")"
echo "  - Server Key:    $(basename "$SERVER_KEY_PEM")"
echo "  - Server CSR:    $(basename "$SERVER_CSR")"
echo "  - Server Certificate: $(basename "$SERVER_CERT")"
echo "  - Certificate Hash: $(cat "$HASH_FILE")"
