#include <arpa/inet.h>
#include <errno.h>
#include <fcntl.h>
#include <stdio.h>
#include <stdlib.h>
#include <sys/socket.h>
#include <unistd.h>

static int denied(int result) {
    return result == -1 && (errno == EACCES || errno == EPERM);
}

static int connect_local(const char *port) {
    int fd = socket(AF_INET, SOCK_STREAM, 0);
    if (fd < 0) return errno;
    struct sockaddr_in name = {
        .sin_family = AF_INET,
        .sin_addr = { .s_addr = htonl(INADDR_LOOPBACK) },
        .sin_port = htons((unsigned short)strtoul(port, NULL, 10)),
    };
    int result = connect(fd, (struct sockaddr *)&name, sizeof(name)) == 0 ? 0 : errno;
    close(fd);
    return result;
}

int main(int argc, char **argv) {
    fputs("fixture entered\n", stderr);
    if (argc != 7) return 90;
    int fd = open(argv[1], O_RDONLY);
    char value = 0;
    if (fd < 0 || read(fd, &value, 1) != 1 || value != 's') return 10;
    close(fd);
    fd = open(argv[2], O_WRONLY | O_CREAT | O_TRUNC, 0600);
    if (fd < 0 || write(fd, "ok", 2) != 2) return 11;
    close(fd);
    if (!denied(open(argv[3], O_RDONLY))) return 12;
    if (!denied(open(argv[3], O_WRONLY | O_TRUNC))) return 13;
    if (!denied(open(argv[4], O_WRONLY | O_CREAT, 0600))) return 14;
    fputs("fixture file boundary checked\n", stderr);
    int broker = connect_local(argv[5]);
    int other = connect_local(argv[6]);
    if (broker != 0 && broker != EPERM && broker != EACCES) return 15;
    if (other != EPERM && other != EACCES) return 16;
    fputs("fixture network boundary checked\n", stderr);
    char *const other_program[] = { "/usr/bin/true", NULL };
    if (!denied(execv(other_program[0], other_program))) return 17;
    puts(broker == 0 ? "fixture pass; broker allowed" : "fixture pass; broker denied");
    return 0;
}
