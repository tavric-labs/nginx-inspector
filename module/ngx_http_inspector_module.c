#include <ngx_config.h>
#include <ngx_core.h>
#include <ngx_http.h>
#include <sys/mman.h>
#include <stdint.h>

/* A best-effort observer. No request-body reads, disk writes or socket retries. */
typedef struct {
    ngx_flag_t enabled;
    ngx_uint_t port;
    ngx_uint_t sample;
    ngx_str_t control_path;
} inspector_conf;

typedef struct {
    ngx_msec_t start_mono;
    ngx_msec_t post_read, rewrite, access, content, header, body;
    ngx_uint_t rewrites;
    off_t body_bytes;
    ngx_uint_t selected;
} inspector_ctx;

typedef struct { u_char *p, *end; ngx_flag_t full; } writer;
static ngx_int_t inspector_init(ngx_conf_t *cf);
static void *inspector_create(ngx_conf_t *cf);
static char *inspector_merge(ngx_conf_t *cf, void *parent, void *child);
static ngx_int_t inspector_worker(ngx_cycle_t *cycle);
static void inspector_exit(ngx_cycle_t *cycle);
static ngx_http_output_header_filter_pt next_header;
static ngx_http_output_body_filter_pt next_body;
static int event_socket = -1;
static ngx_uint_t sent_events, dropped_events;
static ngx_str_t control_path;
static volatile uint64_t *capture_deadline;
extern ngx_module_t ngx_http_inspector_module;

static ngx_command_t commands[] = {
    { ngx_string("inspector"), NGX_HTTP_MAIN_CONF|NGX_HTTP_SRV_CONF|NGX_HTTP_LOC_CONF|NGX_CONF_FLAG,
      ngx_conf_set_flag_slot, NGX_HTTP_LOC_CONF_OFFSET, offsetof(inspector_conf, enabled), NULL },
    { ngx_string("inspector_port"), NGX_HTTP_MAIN_CONF|NGX_HTTP_SRV_CONF|NGX_HTTP_LOC_CONF|NGX_CONF_TAKE1,
      ngx_conf_set_num_slot, NGX_HTTP_LOC_CONF_OFFSET, offsetof(inspector_conf, port), NULL },
    { ngx_string("inspector_sample"), NGX_HTTP_MAIN_CONF|NGX_HTTP_SRV_CONF|NGX_HTTP_LOC_CONF|NGX_CONF_TAKE1,
      ngx_conf_set_num_slot, NGX_HTTP_LOC_CONF_OFFSET, offsetof(inspector_conf, sample), NULL },
    { ngx_string("inspector_control"), NGX_HTTP_MAIN_CONF|NGX_CONF_TAKE1,
      ngx_conf_set_str_slot, NGX_HTTP_LOC_CONF_OFFSET, offsetof(inspector_conf, control_path), NULL },
    ngx_null_command
};
static ngx_http_module_t module_ctx = {
    NULL, inspector_init, NULL, NULL, NULL, NULL, inspector_create, inspector_merge
};
ngx_module_t ngx_http_inspector_module = {
    NGX_MODULE_V1, &module_ctx, commands, NGX_HTTP_MODULE,
    NULL, NULL, inspector_worker, NULL, NULL, inspector_exit, NULL, NGX_MODULE_V1_PADDING
};

static void cleanup(void *data) { (void) data; }

static ngx_flag_t capture_active(void) {
    uint64_t deadline, now;
    if (capture_deadline == NULL) return 0;
    deadline = *capture_deadline;
    now = (uint64_t) ngx_current_msec;
    /* Reject stale or corrupt values; collector renews a 1.5-second lease. */
    return deadline > now && deadline - now <= 2000;
}

/* Internal redirects clear module contexts; request cleanups survive them. */
static inspector_ctx *context(ngx_http_request_t *r, ngx_flag_t create) {
    ngx_pool_cleanup_t *cl;
    inspector_ctx *ctx;
    inspector_conf *cf;
    ngx_time_t *now;
    if (!capture_active()) return NULL;
    for (cl = r->main->pool->cleanup; cl; cl = cl->next) {
        if (cl->handler == cleanup) return cl->data;
    }
    if (!create || r != r->main) return NULL;
    cf = ngx_http_get_module_loc_conf(r, ngx_http_inspector_module);
    if (!cf->enabled) return NULL;
    cl = ngx_pool_cleanup_add(r->main->pool, sizeof(inspector_ctx));
    if (cl == NULL) return NULL;
    cl->handler = cleanup;
    ctx = cl->data;
    ngx_memzero(ctx, sizeof(*ctx));
    now = ngx_timeofday();
    ctx->start_mono = ngx_current_msec - (ngx_msec_t)
        ((now->sec - r->start_sec) * 1000 + now->msec - r->start_msec);
    ctx->selected = ((ngx_uint_t) ngx_random() % 10000) < cf->sample;
    return ctx;
}
static ngx_int_t post_read(ngx_http_request_t *r) {
    inspector_ctx *c = context(r, 1);
    if (c && c->selected) c->post_read = ngx_current_msec;
    return NGX_DECLINED;
}
static ngx_int_t rewrite(ngx_http_request_t *r) {
    inspector_ctx *c = context(r, 1);
    if (c && c->selected && r == r->main) {
        if (!c->rewrite) c->rewrite = ngx_current_msec;
        c->rewrites++;
    }
    return NGX_DECLINED;
}
static ngx_int_t access_phase(ngx_http_request_t *r) {
    inspector_ctx *c = context(r, 1);
    if (c && c->selected && r == r->main && !c->access) c->access = ngx_current_msec;
    return NGX_DECLINED;
}
static ngx_int_t content_phase(ngx_http_request_t *r) {
    inspector_ctx *c = context(r, 1);
    if (c && c->selected && r == r->main && !c->content) c->content = ngx_current_msec;
    return NGX_DECLINED;
}
static ngx_int_t header_filter(ngx_http_request_t *r) {
    inspector_ctx *c = context(r, 1);
    if (c && c->selected && r == r->main && !c->header) c->header = ngx_current_msec;
    return next_header(r);
}
static ngx_int_t body_filter(ngx_http_request_t *r, ngx_chain_t *in) {
    ngx_chain_t *cl;
    inspector_ctx *c = context(r, 0);
    if (c && c->selected && r == r->main) {
        if (!c->body && in) c->body = ngx_current_msec;
        for (cl = in; cl; cl = cl->next) c->body_bytes += ngx_buf_size(cl->buf);
    }
    return next_body(r, in);
}
static void literal(writer *w, const char *s) {
    size_t n = ngx_strlen(s);
    if ((size_t)(w->end - w->p) < n) { w->full = 1; return; }
    w->p = ngx_cpymem(w->p, s, n);
}
static void number(writer *w, int64_t n) {
    u_char b[NGX_INT64_LEN + 2], *p;
    p = ngx_sprintf(b, "%L", n);
    if ((size_t)(w->end - w->p) < (size_t)(p - b)) { w->full = 1; return; }
    w->p = ngx_cpymem(w->p, b, p - b);
}
static void string(writer *w, u_char *s, size_t n) {
    static const char hex[] = "0123456789abcdef";
    size_t i;
    u_char ch;
    literal(w, "\"");
    /* Escape non-ASCII bytes too, so malformed client bytes cannot break JSON. */
    for (i = 0; i < n && i < 256; i++) {
        ch = s[i];
        if (w->end - w->p < 7) { w->full = 1; return; }
        if (ch == '"' || ch == '\\') { *w->p++ = '\\'; *w->p++ = ch; }
        else if (ch < 32 || ch >= 127) {
            *w->p++ = '\\'; *w->p++ = 'u'; *w->p++ = '0'; *w->p++ = '0';
            *w->p++ = hex[ch >> 4]; *w->p++ = hex[ch & 15];
        } else *w->p++ = ch;
    }
    literal(w, "\"");
}
static void variable(writer *w, ngx_http_request_t *r, const char *name) {
    ngx_str_t key;
    ngx_http_variable_value_t *v;
    key.data = (u_char *) name; key.len = ngx_strlen(name);
    v = ngx_http_get_variable(r, &key, ngx_hash_key(key.data, key.len));
    if (v == NULL || v->not_found) literal(w, "null");
    else string(w, v->data, v->len);
}
static void phase(writer *w, ngx_msec_t t, ngx_msec_t start) {
    if (!t) literal(w, "null"); else number(w, (int64_t)(t - start));
}
static ngx_int_t log_request(ngx_http_request_t *r) {
    inspector_ctx *c = context(r, 0);
    inspector_conf *cf = ngx_http_get_module_loc_conf(r, ngx_http_inspector_module);
    struct sockaddr_in dst;
    u_char buf[8192];
    writer w;
    ngx_msec_t start;
    ssize_t n;
    if (r != r->main || !cf->enabled || !c || !c->selected) return NGX_OK;
    w.p = buf; w.end = buf + sizeof(buf); w.full = 0;
    start = (ngx_msec_t) r->start_sec * 1000 + r->start_msec;
    literal(&w, "{\"schema\":1,\"id\":\"");
    number(&w, ngx_pid); literal(&w, "-"); number(&w, r->connection->number);
    literal(&w, "-"); number(&w, r->connection->requests); literal(&w, "-"); number(&w, start);
    literal(&w, "\",\"started_ms\":"); number(&w, start);
    literal(&w, ",\"duration_ms\":"); number(&w, ngx_current_msec - c->start_mono);
    literal(&w, ",\"client\":"); string(&w, r->connection->addr_text.data, r->connection->addr_text.len);
    literal(&w, ",\"method\":"); string(&w, r->method_name.data, r->method_name.len);
    literal(&w, ",\"path\":"); string(&w, r->uri.data, r->uri.len);
    literal(&w, ",\"status\":"); number(&w, r->headers_out.status ? r->headers_out.status : r->err_status);
    literal(&w, ",\"request_bytes\":"); number(&w, r->request_length);
    literal(&w, ",\"response_body_bytes\":"); number(&w, c->body_bytes);
    literal(&w, ",\"wire_bytes\":"); variable(&w, r, "bytes_sent");
    literal(&w, ",\"connection_requests\":"); number(&w, r->connection->requests);
    literal(&w, ",\"rewrite_visits\":"); number(&w, c->rewrites);
    literal(&w, ",\"phases_ms\":{\"post_read\":"); phase(&w, c->post_read, c->start_mono);
    literal(&w, ",\"rewrite\":"); phase(&w, c->rewrite, c->start_mono);
    literal(&w, ",\"access\":"); phase(&w, c->access, c->start_mono);
    literal(&w, ",\"content\":"); phase(&w, c->content, c->start_mono);
    literal(&w, ",\"header_filter\":"); phase(&w, c->header, c->start_mono);
    literal(&w, ",\"first_body_filter\":"); phase(&w, c->body, c->start_mono);
    literal(&w, "},\"request_headers\":{\"content-type\":"); variable(&w, r, "http_content_type");
    literal(&w, ",\"content-length\":"); variable(&w, r, "http_content_length");
    literal(&w, "},\"response_headers\":{\"content-type\":");
    string(&w, r->headers_out.content_type.data, r->headers_out.content_type.len);
    literal(&w, "},\"upstream\":{\"address\":"); variable(&w, r, "upstream_addr");
    literal(&w, ",\"connect_s\":"); variable(&w, r, "upstream_connect_time");
    literal(&w, ",\"header_s\":"); variable(&w, r, "upstream_header_time");
    literal(&w, ",\"response_s\":"); variable(&w, r, "upstream_response_time");
    literal(&w, ",\"status\":"); variable(&w, r, "upstream_status");
    literal(&w, ",\"cache_status\":"); variable(&w, r, "upstream_cache_status");
    literal(&w, "},\"tls\":{\"version\":"); variable(&w, r, "ssl_protocol");
    literal(&w, ",\"cipher\":"); variable(&w, r, "ssl_cipher");
    literal(&w, "},\"worker_sent\":"); number(&w, sent_events);
    literal(&w, ",\"worker_dropped\":"); number(&w, dropped_events);
    literal(&w, "}");
    ngx_memzero(&dst, sizeof(dst)); dst.sin_family = AF_INET;
    dst.sin_port = htons((in_port_t) cf->port); dst.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
    n = w.full || event_socket < 0 ? -1 : sendto(event_socket, buf, w.p - buf, MSG_DONTWAIT,
                                               (struct sockaddr *) &dst, sizeof(dst));
    if (n == w.p - buf) sent_events++; else dropped_events++;
    return NGX_OK;
}
static ngx_int_t add_phase(ngx_conf_t *cf, ngx_uint_t phase_id, ngx_http_handler_pt fn) {
    ngx_http_core_main_conf_t *cm = ngx_http_conf_get_module_main_conf(cf, ngx_http_core_module);
    ngx_http_handler_pt *h = ngx_array_push(&cm->phases[phase_id].handlers);
    if (!h) return NGX_ERROR;
    *h = fn; return NGX_OK;
}
static ngx_int_t inspector_init(ngx_conf_t *cf) {
    inspector_conf *conf = ngx_http_conf_get_module_loc_conf(cf, ngx_http_inspector_module);
    control_path = conf->control_path;
    if (add_phase(cf, NGX_HTTP_POST_READ_PHASE, post_read) != NGX_OK
        || add_phase(cf, NGX_HTTP_REWRITE_PHASE, rewrite) != NGX_OK
        || add_phase(cf, NGX_HTTP_PREACCESS_PHASE, access_phase) != NGX_OK
        || add_phase(cf, NGX_HTTP_PRECONTENT_PHASE, content_phase) != NGX_OK
        || add_phase(cf, NGX_HTTP_LOG_PHASE, log_request) != NGX_OK) return NGX_ERROR;
    next_header = ngx_http_top_header_filter; ngx_http_top_header_filter = header_filter;
    next_body = ngx_http_top_body_filter; ngx_http_top_body_filter = body_filter;
    return NGX_OK;
}
static void *inspector_create(ngx_conf_t *cf) {
    inspector_conf *c = ngx_pcalloc(cf->pool, sizeof(*c));
    if (c) { c->enabled = NGX_CONF_UNSET; c->port = NGX_CONF_UNSET_UINT; c->sample = NGX_CONF_UNSET_UINT; }
    return c;
}
static char *inspector_merge(ngx_conf_t *cf, void *parent, void *child) {
    inspector_conf *p = parent, *c = child;
    ngx_conf_merge_value(c->enabled, p->enabled, 0);
    ngx_conf_merge_uint_value(c->port, p->port, 19876);
    ngx_conf_merge_uint_value(c->sample, p->sample, 10000);
    ngx_conf_merge_str_value(c->control_path, p->control_path, "");
    if (c->port < 1024 || c->port > 65535 || c->sample > 10000) {
        ngx_conf_log_error(NGX_LOG_EMERG, cf, 0, "inspector_port must be 1024..65535; inspector_sample must be 0..10000");
        return NGX_CONF_ERROR;
    }
    return NGX_CONF_OK;
}
static ngx_int_t inspector_worker(ngx_cycle_t *cycle) {
    int fd;
    struct stat info;
    void *mapping;
    if (sizeof(void *) != 8 || control_path.len == 0) {
        ngx_log_error(NGX_LOG_NOTICE, cycle->log, 0, "inspector capture disabled: configure inspector_control on a 64-bit host");
    } else {
        fd = open((char *) control_path.data, O_RDONLY|O_NOFOLLOW|O_CLOEXEC);
        if (fd >= 0) {
            if (fstat(fd, &info) == 0 && S_ISREG(info.st_mode) && info.st_size == 8) {
                mapping = mmap(NULL, 8, PROT_READ, MAP_SHARED, fd, 0);
                if (mapping != MAP_FAILED) capture_deadline = mapping;
            }
            close(fd);
        }
        if (capture_deadline == NULL)
            ngx_log_error(NGX_LOG_WARN, cycle->log, ngx_errno, "inspector control unavailable; capture stays disabled");
    }
    event_socket = socket(AF_INET, SOCK_DGRAM, 0);
    if (event_socket < 0 || ngx_nonblocking(event_socket) == -1) {
        if (event_socket >= 0) close(event_socket);
        event_socket = -1;
        ngx_log_error(NGX_LOG_WARN, cycle->log, ngx_errno, "inspector transport unavailable; requests continue");
    }
    return NGX_OK;
}
static void inspector_exit(ngx_cycle_t *cycle) {
    ngx_log_error(NGX_LOG_NOTICE, cycle->log, 0, "inspector sent=%ui dropped=%ui", sent_events, dropped_events);
    if (event_socket >= 0) close(event_socket);
    if (capture_deadline != NULL) munmap((void *) capture_deadline, 8);
}
