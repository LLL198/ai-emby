/* Private libbluray bridge. The ISO is fetched from the worker's loopback Range server.
 * stdin: READ + uint32 length + uint64 offset (big endian).
 * stdout: BDR1 + uint64 title bytes + uint64 duration ticks + uint32 playlist,
 * then uint32 status + uint32 length + movie bytes for each request.
 * No source image or movie data is written to disk. */
#include <libbluray/bluray.h>
#include <curl/curl.h>
#include <inttypes.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#define MAX_READ (1U << 20)
#define SECTOR 2048U

struct source {
    CURL *curl;
    unsigned char *buffer;
    size_t capacity, used;
};

static uint32_t u32(const unsigned char *p) {
    return (uint32_t)p[0] << 24 | (uint32_t)p[1] << 16 | (uint32_t)p[2] << 8 | p[3];
}
static uint64_t u64(const unsigned char *p) { return (uint64_t)u32(p) << 32 | u32(p + 4); }
static void put32(unsigned char *p, uint32_t n) {
    for (int i = 3; i >= 0; --i) { p[i] = (unsigned char)n; n >>= 8; }
}
static void put64(unsigned char *p, uint64_t n) {
    put32(p, (uint32_t)(n >> 32)); put32(p + 4, (uint32_t)n);
}
static size_t receive(void *data, size_t size, size_t count, void *opaque) {
    struct source *s = opaque;
    if (size && count > SIZE_MAX / size) return 0;
    size_t n = size * count;
    if (n > s->capacity - s->used) return 0;
    memcpy(s->buffer + s->used, data, n);
    s->used += n;
    return n;
}
static int read_blocks(void *opaque, void *buffer, int lba, int count) {
    struct source *s = opaque;
    if (lba < 0 || count <= 0 || count > (int)(MAX_READ / SECTOR)) return 0;
    uint64_t offset = (uint64_t)lba * SECTOR;
    size_t length = (size_t)count * SECTOR;
    char range[80];
    snprintf(range, sizeof(range), "%" PRIu64 "-%" PRIu64, offset, offset + length - 1);
    s->buffer = buffer; s->capacity = length; s->used = 0;
    curl_easy_setopt(s->curl, CURLOPT_RANGE, range);
    CURLcode result = curl_easy_perform(s->curl);
    long status = 0;
    curl_easy_getinfo(s->curl, CURLINFO_RESPONSE_CODE, &status);
    if (result != CURLE_OK || status != 206 || s->used != length) return 0;
    return count;
}

static int seek_exact(BLURAY *bd, uint64_t offset, unsigned char *buffer) {
    uint64_t position = bd_tell(bd);
    if (position == offset) return 1;
    position = bd_seek(bd, offset);
    if (position > offset) return 0;
    while (position < offset) {
        int size = offset - position > MAX_READ ? (int)MAX_READ : (int)(offset - position);
        int n = bd_read(bd, buffer, size);
        if (n <= 0) return 0;
        position += (uint64_t)n;
    }
    return 1;
}

int main(int argc, char **argv) {
    if (argc != 2 || strncmp(argv[1], "http://127.0.0.1:", 17) != 0) return 2;
    if (curl_global_init(CURL_GLOBAL_DEFAULT) != CURLE_OK) return 3;
    struct source source = {0};
    source.curl = curl_easy_init();
    BLURAY *bd = bd_init();
    unsigned char *buffer = malloc(MAX_READ);
    int exit_code = 1;
    if (!source.curl || !bd || !buffer) goto done;
    curl_easy_setopt(source.curl, CURLOPT_URL, argv[1]);
    curl_easy_setopt(source.curl, CURLOPT_PROXY, "");
    curl_easy_setopt(source.curl, CURLOPT_FOLLOWLOCATION, 0L);
    curl_easy_setopt(source.curl, CURLOPT_NOSIGNAL, 1L);
    /* The worker enforces upstream deadlines and pause/cancel. Do not time out a paused source here. */
    curl_easy_setopt(source.curl, CURLOPT_CONNECTTIMEOUT, 5L);
    curl_easy_setopt(source.curl, CURLOPT_WRITEFUNCTION, receive);
    curl_easy_setopt(source.curl, CURLOPT_WRITEDATA, &source);
    if (!bd_open_stream(bd, &source, read_blocks)) goto done;
    const BLURAY_DISC_INFO *disc = bd_get_disc_info(bd);
    if (!disc || disc->aacs_detected || disc->bdplus_detected) goto done;
    uint32_t count = bd_get_titles(bd, TITLES_ALL, 0);
    if (count == 0 || count > 8192) goto done;
    uint32_t playlist = 0;
    uint64_t duration = 0;
    for (uint32_t i = 0; i < count; ++i) {
        BLURAY_TITLE_INFO *info = bd_get_title_info(bd, i, 0);
        if (!info) continue;
        if (info->duration > duration) { duration = info->duration; playlist = info->playlist; }
        bd_free_title_info(info);
    }
    if (!duration || !bd_select_playlist(bd, playlist)) goto done;
    bd_select_angle(bd, 0);
    uint64_t title_size = bd_get_title_size(bd);
    if (!title_size || title_size > INT64_MAX) goto done;
    unsigned char header[24] = {'B','D','R','1'};
    put64(header + 4, title_size); put64(header + 12, duration); put32(header + 20, playlist);
    if (fwrite(header, 1, sizeof(header), stdout) != sizeof(header) || fflush(stdout)) goto done;
    for (;;) {
        unsigned char request[16];
        size_t received = fread(request, 1, sizeof(request), stdin);
        if (!received && feof(stdin)) { exit_code = 0; break; }
        if (received != sizeof(request) || memcmp(request, "READ", 4)) break;
        uint32_t length = u32(request + 4);
        uint64_t offset = u64(request + 8);
        if (!length || length > MAX_READ || offset > title_size || length > title_size - offset) break;
        uint32_t used = 0, status = 0;
        if (!seek_exact(bd, offset, buffer)) status = 1;
        while (!status && used < length) {
            int n = bd_read(bd, buffer + used, (int)(length - used));
            if (n <= 0) { status = 1; break; }
            used += (uint32_t)n;
        }
        unsigned char reply[8];
        put32(reply, status); put32(reply + 4, used);
        if (fwrite(reply, 1, sizeof(reply), stdout) != sizeof(reply) ||
            fwrite(buffer, 1, used, stdout) != used || fflush(stdout)) break;
        if (status) break;
    }
done:
    free(buffer);
    if (bd) bd_close(bd);
    if (source.curl) curl_easy_cleanup(source.curl);
    curl_global_cleanup();
    return exit_code;
}
