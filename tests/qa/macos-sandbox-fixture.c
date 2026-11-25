#include <arpa/inet.h>
#include <errno.h>
#include <fcntl.h>
#include <limits.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
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

static int app_sandbox_fixture(char **argv) {
    char path[PATH_MAX];
    size_t length = confstr(_CS_DARWIN_USER_TEMP_DIR, path, sizeof(path));
    if (length < 2 || length + sizeof("workbench.XXXXXX") > sizeof(path)) return 30;
    memcpy(path + length - 1, "workbench.XXXXXX", sizeof("workbench.XXXXXX"));
    int fd = mkstemp(path);
    if (fd < 0) return 31;
    char value = 0;
    int ok = write(fd, "s", 1) == 1 && lseek(fd, 0, SEEK_SET) == 0 &&
        read(fd, &value, 1) == 1 && value == 's';
    close(fd);
    if (unlink(path) != 0 || !ok) return 32;
    if (!denied(open(argv[2], O_RDONLY)) || !denied(open(argv[2], O_WRONLY | O_TRUNC))) return 33;
    if (!denied(open(argv[3], O_WRONLY | O_CREAT, 0600))) return 34;
    int first = connect_local(argv[4]), second = connect_local(argv[5]);
    if ((first != EPERM && first != EACCES) || (second != EPERM && second != EACCES)) return 35;
    puts("fixture pass; App Sandbox temp, file and network boundaries");
    return 0;
}

static int app_sandbox_node_fixture(char **argv) {
    char directory[PATH_MAX];
    size_t length = confstr(_CS_DARWIN_USER_TEMP_DIR, directory, sizeof(directory));
    if (length < 2 || length > sizeof(directory) || chdir(directory) != 0) return 40;
    if (setenv("ZTAPI_QA_OUTSIDE", argv[4], 1) != 0 ||
        setenv("ZTAPI_QA_NEW_OUTSIDE", argv[5], 1) != 0 ||
        setenv("ZTAPI_QA_PORT", argv[6], 1) != 0 ||
        setenv("TMPDIR", directory, 1) != 0) return 41;
    char *const args[] = {argv[2], "--test", "--test-isolation=none", argv[3], NULL};
    execv(args[0], args);
    perror("node fixture exec");
    return 42;
}

int main(int argc, char **argv) {
    if (argc == 6 && strcmp(argv[1], "--app-sandbox") == 0) return app_sandbox_fixture(argv);
    if (argc == 7 && strcmp(argv[1], "--app-sandbox-node") == 0) return app_sandbox_node_fixture(argv);
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
