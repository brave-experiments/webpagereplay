import socket
import threading
import logging
import select # Corrected import.

logging.basicConfig(level=logging.INFO, format='%(asctime)s - %(levelname)s - %(message)s')

def handle_client(client_socket, client_address):
    try:
        data = client_socket.recv(4096)
        if not data:
            return

        request_lines = data.decode('utf-8').split('\r\n')
        request_line = request_lines[0]
        method, url, http_version = request_line.split(' ', 2)

        logging.info(f"Received request from {client_address}: {method} {url}")

        # Extract hostname and port from URL (simplified for demonstration)
        if method == "CONNECT":
            hostname, port = url.split(":")
            port = int(port)
            forward_socket = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
            forward_socket.connect(("localhost", 8001))
            client_socket.send(b"HTTP/1.1 200 Connection Established\r\n\r\n")
            forward_connection(client_socket, forward_socket)
            return

        else:
            hostname = url.split("//")[1].split("/")[0]
            if ":" in hostname:
                hostname, port = hostname.split(":")
                port = int(port)
            else:
                port = 80 if method == "GET" else 443

            forward_socket = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
            try:
                forward_socket.connect(("localhost", 8000))
            except Exception as e:
                logging.error(f"Error connecting to {hostname}:{port}: {e}")
                client_socket.close()
                return

            forward_socket.send(data)
            forward_connection(client_socket, forward_socket)

    except Exception as e:
        logging.error(f"Error handling client {client_address}: {e}")
    finally:
        client_socket.close()

def forward_connection(client_socket, forward_socket):
    sockets = [client_socket, forward_socket]
    while True:
        try:
            readable, _, _ = select.select(sockets, [], []) # Corrected line.
            for sock in readable:
                data = sock.recv(4096)
                if not data:
                    return
                if sock is client_socket:
                    forward_socket.send(data)
                else:
                    client_socket.send(data)
        except Exception as e:
            logging.error(f"Error forwarding: {e}")
            return

def main():
    host = '0.0.0.0'
    port = 8080  # Choose a port

    server_socket = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    server_socket.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    server_socket.bind((host, port))
    server_socket.listen(5)

    logging.info(f"Proxy server listening on {host}:{port}")

    while True:
        client_socket, client_address = server_socket.accept()
        logging.info(f"Accepted connection from {client_address}")
        client_thread = threading.Thread(target=handle_client, args=(client_socket, client_address))
        client_thread.start()

if __name__ == "__main__":
    main()
