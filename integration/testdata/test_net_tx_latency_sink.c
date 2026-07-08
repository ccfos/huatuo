#include <arpa/inet.h>
#include <netinet/in.h>
#include <stdio.h>
#include <sys/socket.h>
#include <unistd.h>

#define BIND_PORT 19877

int main(void)
{
	int lfd = socket(AF_INET, SOCK_STREAM, 0);
	if (lfd < 0) {
		perror("socket");
		return 1;
	}

	int opt = 1;
	setsockopt(lfd, SOL_SOCKET, SO_REUSEADDR, &opt, sizeof(opt));

	// Bind to any address so the same binary works regardless of which
	// netns / IP the test places it under.
	struct sockaddr_in addr = {
		.sin_family = AF_INET,
		.sin_port = htons(BIND_PORT),
		.sin_addr.s_addr = htonl(INADDR_ANY),
	};
	if (bind(lfd, (struct sockaddr *)&addr, sizeof(addr)) < 0) {
		perror("bind");
		return 1;
	}
	if (listen(lfd, 5) < 0) {
		perror("listen");
		return 1;
	}

	// Drain whatever the client sends so the test can push a lot of TX data
	// through the veth, then close and accept the next connection.
	for (;;) {
		int cfd = accept(lfd, NULL, NULL);
		if (cfd < 0)
			continue;

		char buf[65536];
		while (recv(cfd, buf, sizeof(buf), 0) > 0) {
		}
		close(cfd);
	}
}
