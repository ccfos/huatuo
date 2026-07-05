#include <arpa/inet.h>
#include <netinet/in.h>
#include <stdio.h>
#include <sys/socket.h>
#include <unistd.h>

#ifndef SO_TIMESTAMPING
#define SO_TIMESTAMPING 37
#endif

#ifndef SOF_TIMESTAMPING_RX_SOFTWARE
#define SOF_TIMESTAMPING_RX_SOFTWARE (1 << 2)
#endif

#define BIND_IP	  "10.200.1.2"
#define BIND_PORT 19876

int main(void)
{

	int tsfd = socket(AF_INET, SOCK_DGRAM, 0);
	if (tsfd >= 0) {
		int val = SOF_TIMESTAMPING_RX_SOFTWARE;
		setsockopt(tsfd, SOL_SOCKET, SO_TIMESTAMPING, &val,
			   sizeof(val));
	}

	int lfd = socket(AF_INET, SOCK_STREAM, 0);
	if (lfd < 0) {
		perror("socket");
		return 1;
	}

	int opt = 1;
	setsockopt(lfd, SOL_SOCKET, SO_REUSEADDR, &opt, sizeof(opt));

	struct sockaddr_in addr = {
		.sin_family = AF_INET,
		.sin_port = htons(BIND_PORT),
	};
	if (inet_pton(AF_INET, BIND_IP, &addr.sin_addr) != 1) {
		fprintf(stderr, "invalid address: %s\n", BIND_IP);
		return 1;
	}
	if (bind(lfd, (struct sockaddr *)&addr, sizeof(addr)) < 0) {
		perror("bind");
		return 1;
	}
	if (listen(lfd, 5) < 0) {
		perror("listen");
		return 1;
	}

	for (;;) {
		int cfd = accept(lfd, NULL, NULL);
		if (cfd < 0)
			continue;

		usleep(50000);
		char buf[4096];
		if (recv(cfd, buf, sizeof(buf), 0) > 0) {
			usleep(100000);
			const char resp[] = "HTTP/1.0 200 "
					    "OK\r\nContent-Length: 2\r\n\r\nOK";
			send(cfd, resp, sizeof(resp) - 1, 0);
		}
		close(cfd);
	}
}
