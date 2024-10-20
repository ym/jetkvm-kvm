# Exit immediately if a command exits with a non-zero status
set -e

# Function to display help message
show_help() {
    echo "Usage: $0 [options] -h <host_ip> -r <remote_ip>"
    echo
    echo "Required:"
    echo "  -h, --host <host_ip>      Local host IP address"
    echo "  -r, --remote <remote_ip>   Remote host IP address"
    echo
    echo "Optional:"
    echo "  -u, --user <remote_user>   Remote username (default: root)"
    echo "  -p, --port <port>          Python server port (default: 8000)"
    echo "      --help                 Display this help message"
    echo
    echo "Example:"
    echo "  $0 -h 192.168.0.13 -r 192.168.0.17"
    echo "  $0 -h 192.168.0.13 -r 192.168.0.17 -u admin -p 8080"
    exit 0
}

# Default values
PYTHON_PORT=8000
REMOTE_USER="root"
REMOTE_PATH="/userdata/jetkvm/bin"

# Parse command line arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        -h|--host)
            HOST_IP="$2"
            shift 2
            ;;
        -r|--remote)
            REMOTE_HOST="$2"
            shift 2
            ;;
        -u|--user)
            REMOTE_USER="$2"
            shift 2
            ;;
        -p|--port)
            PYTHON_PORT="$2"
            shift 2
            ;;
        --help)
            show_help
            exit 0
            ;;
        *)
            echo "Unknown option: $1"
            show_help
            exit 1
            ;;
    esac
done

# Verify required parameters
if [ -z "$HOST_IP" ] || [ -z "$REMOTE_HOST" ]; then
    echo "Error: Host IP and Remote IP are required parameters"
    show_help
fi

# Build the development version on the host
make frontend
make build_dev

# Change directory to the binary output directory
cd bin

# Start a Python HTTP server in the background to serve files
python3 -m http.server "$PYTHON_PORT" &
PYTHON_SERVER_PID=$!

# Ensure that the Python server is terminated if the script exits unexpectedly
trap "echo 'Terminating Python server...'; kill $PYTHON_SERVER_PID" EXIT

# Deploy and run the application on the remote host
ssh "${REMOTE_USER}@${REMOTE_HOST}" ash <<EOF
set -e

# Set the library path to include the directory where librockit.so is located
export LD_LIBRARY_PATH=/oem/usr/lib:\$LD_LIBRARY_PATH

# Kill any existing instances of the application
killall jetkvm_app || true
killall jetkvm_app_debug || true

# Navigate to the directory where the binary will be stored
cd "$REMOTE_PATH"

# Remove any old binary
rm -f jetkvm_app

# Download the new binary from the host
wget ${HOST_IP}:${PYTHON_PORT}/jetkvm_app

# Make the new binary executable
chmod +x jetkvm_app

# Rename the binary to jetkvm_app_debug
mv jetkvm_app jetkvm_app_debug

# Run the application in the background
./jetkvm_app_debug

EOF

# Once the SSH session finishes, shut down the Python server
kill "$PYTHON_SERVER_PID"
echo "Deployment complete."
