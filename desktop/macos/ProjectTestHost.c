#include <limits.h>
#include <mach-o/dyld.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

int main(int argc, char **argv) {
    (void)argv;
    if (argc != 1) return 10;
    char executable[PATH_MAX], directory[PATH_MAX], node[PATH_MAX], worker[PATH_MAX], tmp[PATH_MAX];
    uint32_t size = sizeof(executable);
    if (_NSGetExecutablePath(executable, &size) != 0 || realpath(executable, directory) == NULL) return 11;
    char *last = strrchr(directory, '/');
    if (last == NULL) return 11;
    *last = '\0';
    if (snprintf(node, sizeof(node), "%s/node", directory) >= sizeof(node) ||
        snprintf(worker, sizeof(worker), "%s/../Resources/project-test-worker.mjs", directory) >= sizeof(worker)) return 11;
    size_t length = confstr(_CS_DARWIN_USER_TEMP_DIR, tmp, sizeof(tmp));
    if (length < 2 || length > sizeof(tmp) || chdir(tmp) != 0 || setenv("ZTAPI_TEST_TMPDIR", tmp, 1) != 0) return 12;
    char *const args[] = {node, worker, NULL};
    execv(node, args);
    return 13;
}
