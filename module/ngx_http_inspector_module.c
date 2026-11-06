#include <ngx_config.h>
#include <ngx_core.h>
#include <ngx_http.h>
#include <sys/mman.h>
#include <stdint.h>

#define INSPECTOR_TRACE_MAX 40
#define INSPECTOR_SUB_MAX 8
#define INSPECTOR_ATTEMPT_MAX 8
#define INSPECTOR_PAYLOAD_MAX 16384

enum {
    OBS_POST_READ, OBS_SERVER_REWRITE, OBS_REWRITE, OBS_PREACCESS, OBS_ACCESS,
    OBS_PRECONTENT, OBS_HEADERS, OBS_BODY_FIRST, OBS_BODY_LAST, OBS_BODY_STATE,
    OBS_REQUEST_BODY_FIRST, OBS_REQUEST_BODY_LAST, OBS_REQUEST_BODY_RETURN, OBS_LOG,
    OBS_HEADERS_RETURN, OBS_REQUEST_BODY_SPOOL
};
static const char *observation_names[] = {
    "post_read", "server_rewrite", "location_rewrite", "preaccess", "access",
    "precontent", "header_filter", "body_first_buffer", "body_last_buffer", "body_filter_state",
    "request_body_first_buffer", "request_body_last_buffer", "request_body_filter_return", "log",
    "header_filter_return", "request_body_spooled"
};
typedef struct {
    ngx_uint_t event, subrequest, io, request_buffered, connection_buffered, status;
    ngx_msec_t at;
    ngx_int_t result;
    ngx_str_t path, location;
} inspector_observation;
typedef struct {
    ngx_http_request_t *request;
    ngx_msec_t first, last, header;
    off_t body_bytes;
    ngx_str_t initial_path;
} inspector_subrequest;

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
    off_t body_bytes, declared_body_bytes;
    ngx_uint_t selected;
    ngx_uint_t serial, connection_requests, sample;
    ngx_str_t original_path, initial_path;
    inspector_observation *trace;
    inspector_subrequest *subs;
    ngx_uint_t trace_count, trace_omitted, sub_count, sub_omitted;
    ngx_uint_t body_calls, body_buffers, body_flushes, body_syncs;
    off_t memory_bytes, file_bytes, request_body_bytes, temp_file_offset;
    ngx_uint_t request_body_calls, request_body_buffers;
    ngx_uint_t request_buffered_seen, connection_buffered_seen, cached_peer_seen, request_spool_seen;
    ngx_msec_t request_body_first, request_body_last, body_last;
    ngx_int_t body_result, header_result;
    ngx_uint_t last_io;
} inspector_ctx;

typedef struct { u_char *p, *end; ngx_flag_t full; } writer;
static ngx_int_t inspector_init(ngx_conf_t *cf);
static void *inspector_create(ngx_conf_t *cf);
static char *inspector_merge(ngx_conf_t *cf, void *parent, void *child);
static ngx_int_t inspector_worker(ngx_cycle_t *cycle);
static void inspector_exit(ngx_cycle_t *cycle);
static ngx_http_output_header_filter_pt next_header;
static ngx_http_output_body_filter_pt next_body;
static ngx_http_request_body_filter_pt next_request_body;
static int event_socket = -1;
static ngx_uint_t sent_events, dropped_events;
static ngx_int_t gzip_ratio_index = NGX_ERROR;
static ngx_int_t limit_req_index = NGX_ERROR, limit_conn_index = NGX_ERROR;
static ngx_str_t control_path;
static volatile uint64_t *capture_deadline;
static ngx_uint_t request_serial, worker_connection_limit, configured_workers;
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
    deadline = __atomic_load_n(capture_deadline, __ATOMIC_ACQUIRE);
    now = (uint64_t) ngx_current_msec;
    /* Reject stale or corrupt values; collector renews a 1.5-second lease. */
    return deadline > now && deadline - now <= 2000;
}

static ngx_str_t clipped_string(ngx_pool_t *pool, ngx_str_t value, size_t cap) {
    ngx_str_t result = ngx_null_string;
    result.len = ngx_min(value.len, cap);
    if (result.len) {
        result.data = ngx_pnalloc(pool, result.len);
        if (result.data) ngx_memcpy(result.data, value.data, result.len);
        else result.len = 0;
    }
    return result;
}

/* Bit definitions are documented in docs/native-observations.md. No syscalls. */
static ngx_uint_t io_state(ngx_http_request_t *r) {
    ngx_connection_t *client = r->connection, *up;
    ngx_uint_t bits = 0;
    if (client->read->ready) bits |= 1;
    if (client->write->ready) bits |= 2;
    if (client->write->delayed) bits |= 4;
    if (client->write->timer_set) bits |= 8;
    if (client->read->timer_set) bits |= 16;
    if (client->error) bits |= 32;
    if (client->read->timedout) bits |= 32768;
    if (client->write->timedout) bits |= 65536;
    if (r->upstream && (up = r->upstream->peer.connection) != NULL) {
        bits |= 64;
        if (up->read->ready) bits |= 128;
        if (up->write->ready) bits |= 256;
        if (up->read->timer_set) bits |= 512;
        if (up->write->timer_set) bits |= 1024;
        if (up->read->eof) bits |= 2048;
        if (up->error) bits |= 4096;
        if (up->read->timedout) bits |= 131072;
        if (up->write->timedout) bits |= 262144;
    }
    if (r->upstream && r->upstream->pipe && r->upstream->pipe->upstream_blocked) bits |= 8192;
    return bits;
}

/* Internal redirects clear module contexts; request cleanups survive them. */
static inspector_ctx *context(ngx_http_request_t *r, ngx_flag_t create) {
    ngx_pool_cleanup_t *cl;
    inspector_ctx *ctx;
    inspector_conf *cf;
    ngx_time_t *now;
    if (!capture_active()) return NULL;
    ctx = ngx_http_get_module_ctx(r->main, ngx_http_inspector_module);
    if (ctx) return ctx;
    for (cl = r->main->pool->cleanup; cl; cl = cl->next) {
        if (cl->handler == cleanup) {
            ngx_http_set_ctx(r->main, cl->data, ngx_http_inspector_module);
            return cl->data;
        }
    }
    if (!create || r != r->main) return NULL;
    cf = ngx_http_get_module_loc_conf(r, ngx_http_inspector_module);
    if (!cf->enabled) return NULL;
    cl = ngx_pool_cleanup_add(r->main->pool, sizeof(inspector_ctx));
    if (cl == NULL) return NULL;
    cl->handler = cleanup;
    ctx = cl->data;
    ngx_http_set_ctx(r->main, ctx, ngx_http_inspector_module);
    ngx_memzero(ctx, sizeof(*ctx));
    now = ngx_timeofday();
    ctx->start_mono = ngx_current_msec - (ngx_msec_t)
        ((now->sec - r->start_sec) * 1000 + now->msec - r->start_msec);
    ctx->selected = ((ngx_uint_t) ngx_random() % 10000) < cf->sample;
    ctx->serial = ++request_serial;
    ctx->connection_requests = r->connection->requests;
    ctx->sample = cf->sample;
    ctx->declared_body_bytes = r->headers_in.content_length_n;
    if (ctx->selected) {
        ctx->trace = ngx_pcalloc(r->pool, INSPECTOR_TRACE_MAX * sizeof(inspector_observation));
        ctx->subs = ngx_pcalloc(r->pool, INSPECTOR_SUB_MAX * sizeof(inspector_subrequest));
        ctx->initial_path = clipped_string(r->pool, r->uri, 256);
        ctx->original_path = r->unparsed_uri;
        {
            size_t i;
            for (i = 0; i < ctx->original_path.len; i++) {
                if (ctx->original_path.data[i] == '?') { ctx->original_path.len = i; break; }
            }
        }
        ctx->original_path = clipped_string(r->pool, ctx->original_path, 256);
    }
    return ctx;
}

static void observe(inspector_ctx *c, ngx_http_request_t *r, ngx_uint_t event, ngx_int_t result) {
    inspector_observation *o;
    inspector_subrequest *s = NULL;
    ngx_http_core_loc_conf_t *location;
    ngx_uint_t i, sub = 0;
    if (!c || !c->selected) return;
    if (r != r->main) {
        for (i = 0; i < c->sub_count; i++) if (c->subs[i].request == r) { s = &c->subs[i]; sub = i + 1; break; }
        if (!s) {
            if (!c->subs || c->sub_count == INSPECTOR_SUB_MAX) { c->sub_omitted++; return; }
            sub = ++c->sub_count; s = &c->subs[sub - 1]; s->request = r;
            s->first = ngx_current_msec; s->initial_path = clipped_string(r->main->pool, r->uri, 128);
        }
        s->last = ngx_current_msec;
        if (event == OBS_HEADERS && !s->header) s->header = ngx_current_msec;
    }
    if (r == r->main && r->upstream) {
        if (r->upstream->peer.cached) c->cached_peer_seen = 1;
        if (r->upstream->pipe && r->upstream->pipe->temp_file)
            c->temp_file_offset = ngx_max(c->temp_file_offset, r->upstream->pipe->temp_file->offset);
    }
    if (!c->trace || c->trace_count == INSPECTOR_TRACE_MAX) { c->trace_omitted++; return; }
    o = &c->trace[c->trace_count++]; o->event = event; o->subrequest = sub;
    o->at = ngx_current_msec; o->result = result; o->io = io_state(r);
    o->status = r->headers_out.status ? r->headers_out.status : r->err_status;
    o->request_buffered = r->buffered; o->connection_buffered = r->connection->buffered;
    if (r == r->main) c->last_io = o->io;
    if (event == OBS_REWRITE) {
        location = ngx_http_get_module_loc_conf(r, ngx_http_core_module);
        o->path = clipped_string(r->main->pool, r->uri, 128);
        o->location = clipped_string(r->main->pool, location->name, 128);
    }
}

static ngx_int_t post_read(ngx_http_request_t *r) {
    inspector_ctx *c = context(r, 1);
    if (c && c->selected && r == r->main) c->post_read = ngx_current_msec;
    observe(c, r, OBS_POST_READ, NGX_OK);
    return NGX_DECLINED;
}
static ngx_int_t server_rewrite(ngx_http_request_t *r) {
    observe(context(r, 1), r, OBS_SERVER_REWRITE, NGX_OK); return NGX_DECLINED;
}
static ngx_int_t rewrite(ngx_http_request_t *r) {
    inspector_ctx *c = context(r, 1);
    if (c && c->selected && r == r->main) {
        if (!c->rewrite) c->rewrite = ngx_current_msec;
        c->rewrites++;
    }
    observe(c, r, OBS_REWRITE, NGX_OK);
    return NGX_DECLINED;
}
static ngx_int_t access_phase(ngx_http_request_t *r) {
    inspector_ctx *c = context(r, 1);
    if (c && c->selected && r == r->main && !c->access) c->access = ngx_current_msec;
    observe(c, r, OBS_PREACCESS, NGX_OK);
    return NGX_DECLINED;
}
static ngx_int_t actual_access(ngx_http_request_t *r) {
    observe(context(r, 1), r, OBS_ACCESS, NGX_OK); return NGX_DECLINED;
}
static ngx_int_t content_phase(ngx_http_request_t *r) {
    inspector_ctx *c = context(r, 1);
    if (c && c->selected && r == r->main && !c->content) c->content = ngx_current_msec;
    observe(c, r, OBS_PRECONTENT, NGX_OK);
    return NGX_DECLINED;
}
static ngx_int_t header_filter(ngx_http_request_t *r) {
    ngx_int_t rc;
    inspector_ctx *c = context(r, 1);
    if (c && c->selected && r == r->main && !c->header) c->header = ngx_current_msec;
    observe(c, r, OBS_HEADERS, NGX_OK);
    rc = next_header(r);
    if (c && c->selected && capture_active()) {
        if (r == r->main) c->header_result = rc;
        observe(c, r, OBS_HEADERS_RETURN, rc);
    }
    return rc;
}
static ngx_int_t body_filter(ngx_http_request_t *r, ngx_chain_t *in) {
    ngx_chain_t *cl;
    ngx_int_t rc;
    ngx_uint_t i, final = 0;
    off_t size;
    inspector_ctx *c = context(r, 0);
    if (c && c->selected && r == r->main) {
        c->body_calls++;
        if (!c->body && in) { c->body = ngx_current_msec; observe(c, r, OBS_BODY_FIRST, NGX_OK); }
        for (cl = in; cl; cl = cl->next) {
            size = ngx_buf_size(cl->buf); c->body_bytes += size; c->body_buffers++;
            if (ngx_buf_in_memory(cl->buf)) c->memory_bytes += size;
            else if (cl->buf->in_file) c->file_bytes += size;
            c->body_flushes += cl->buf->flush; c->body_syncs += cl->buf->sync;
            if (cl->buf->last_buf) final = 1;
        }
        c->body_last = ngx_current_msec;
        if (final) observe(c, r, OBS_BODY_LAST, NGX_OK);
    } else if (c && c->selected && r != r->main) {
        observe(c, r, OBS_BODY_FIRST, NGX_OK);
        for (i = 0; i < c->sub_count; i++) if (c->subs[i].request == r)
            for (cl = in; cl; cl = cl->next) c->subs[i].body_bytes += ngx_buf_size(cl->buf);
    }
    rc = next_body(r, in);
    if (c && c->selected && r == r->main && capture_active()) {
        c->body_result = rc;
        c->request_buffered_seen |= r->buffered;
        c->connection_buffered_seen |= r->connection->buffered;
        if (rc != NGX_OK || io_state(r) != c->last_io || r->buffered || r->connection->buffered)
            observe(c, r, OBS_BODY_STATE, rc);
    }
    return rc;
}

static ngx_int_t request_body_filter(ngx_http_request_t *r, ngx_chain_t *in) {
    ngx_chain_t *cl;
    ngx_int_t rc;
    ngx_uint_t final = 0;
    inspector_ctx *c = context(r, 0);
    if (c && c->selected && r == r->main) {
        c->request_body_calls++;
        if (!c->request_body_first && in) {
            c->request_body_first = ngx_current_msec;
            observe(c, r, OBS_REQUEST_BODY_FIRST, NGX_OK);
        }
        for (cl = in; cl; cl = cl->next) {
            c->request_body_bytes += ngx_buf_size(cl->buf); c->request_body_buffers++;
            if (cl->buf->last_buf) final = 1;
        }
        c->request_body_last = ngx_current_msec;
        if (final) observe(c, r, OBS_REQUEST_BODY_LAST, NGX_OK);
    }
    rc = next_request_body(r, in);
    if (c && c->selected && r == r->main && r->request_body && r->request_body->temp_file && !c->request_spool_seen) {
        c->request_spool_seen = 1;
        observe(c, r, OBS_REQUEST_BODY_SPOOL, rc);
    }
    if (c && c->selected && r == r->main && rc != NGX_OK)
        observe(c, r, OBS_REQUEST_BODY_RETURN, rc);
    return rc;
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
static void optional_variable(writer *w, ngx_http_request_t *r, ngx_int_t index) {
    ngx_http_variable_value_t *value;
    if (index == NGX_ERROR) { literal(w, "null"); return; }
    value = ngx_http_get_indexed_variable(r, index);
    if (value == NULL || value->not_found) literal(w, "null");
    else string(w, value->data, value->len);
}
static void phase(writer *w, ngx_msec_t t, ngx_msec_t start) {
    if (!t) literal(w, "null"); else number(w, (int64_t)(t - start));
}
#include "inspector_native_json.h"
static ngx_int_t log_request(ngx_http_request_t *r) {
    inspector_ctx *c = context(r, 0);
    inspector_conf *cf = ngx_http_get_module_loc_conf(r, ngx_http_inspector_module);
    struct sockaddr_in dst;
    u_char buf[INSPECTOR_PAYLOAD_MAX];
    writer w;
    ngx_msec_t start;
    ssize_t n;
    if (r != r->main || !cf->enabled || !c || !c->selected) return NGX_OK;
    observe(c, r, OBS_LOG, NGX_OK);
    w.p = buf; w.end = buf + sizeof(buf); w.full = 0;
    start = (ngx_msec_t) r->start_sec * 1000 + r->start_msec;
    literal(&w, "{\"schema\":2,\"id\":\"");
    number(&w, ngx_pid); literal(&w, "-"); number(&w, c->serial);
    literal(&w, "-"); number(&w, r->connection->number); literal(&w, "-"); number(&w, start);
    literal(&w, "\",\"started_ms\":"); number(&w, start);
    literal(&w, ",\"duration_ms\":"); number(&w, ngx_current_msec - c->start_mono);
    literal(&w, ",\"client\":"); string(&w, r->connection->addr_text.data, r->connection->addr_text.len);
    literal(&w, ",\"method\":"); string(&w, r->method_name.data, r->method_name.len);
    literal(&w, ",\"path\":"); string(&w, r->uri.data, r->uri.len);
    literal(&w, ",\"status\":"); number(&w, r->headers_out.status ? r->headers_out.status : r->err_status);
    literal(&w, ",\"request_bytes\":"); number(&w, r->request_length);
    literal(&w, ",\"response_body_bytes\":"); number(&w, c->body_bytes);
    literal(&w, ",\"wire_bytes\":"); variable(&w, r, "bytes_sent");
    literal(&w, ",\"connection_requests\":"); number(&w, c->connection_requests);
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
    native_json(&w, r, c);
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
    ngx_http_core_main_conf_t *main_conf = ngx_http_conf_get_module_main_conf(cf, ngx_http_core_module);
    ngx_hash_key_t *keys = main_conf->variables_keys->keys.elts;
    ngx_str_t names[] = { ngx_string("gzip_ratio"), ngx_string("limit_req_status"), ngx_string("limit_conn_status") };
    ngx_int_t *indexes[] = { &gzip_ratio_index, &limit_req_index, &limit_conn_index };
    ngx_uint_t i, j;
    /* Register optional NOHASH variables without requiring absent modules. */
    for (j = 0; j < 3; j++) {
        *indexes[j] = NGX_ERROR;
        for (i = 0; i < main_conf->variables_keys->keys.nelts; i++) {
            if (keys[i].key.len == names[j].len && ngx_strncmp(keys[i].key.data, names[j].data, names[j].len) == 0) {
                *indexes[j] = ngx_http_get_variable_index(cf, &names[j]);
                if (*indexes[j] == NGX_ERROR) return NGX_ERROR;
                break;
            }
        }
    }
    control_path = conf->control_path;
    if (add_phase(cf, NGX_HTTP_POST_READ_PHASE, post_read) != NGX_OK
        || add_phase(cf, NGX_HTTP_SERVER_REWRITE_PHASE, server_rewrite) != NGX_OK
        || add_phase(cf, NGX_HTTP_REWRITE_PHASE, rewrite) != NGX_OK
        || add_phase(cf, NGX_HTTP_PREACCESS_PHASE, access_phase) != NGX_OK
        || add_phase(cf, NGX_HTTP_ACCESS_PHASE, actual_access) != NGX_OK
        || add_phase(cf, NGX_HTTP_PRECONTENT_PHASE, content_phase) != NGX_OK
        || add_phase(cf, NGX_HTTP_LOG_PHASE, log_request) != NGX_OK) return NGX_ERROR;
    next_header = ngx_http_top_header_filter; ngx_http_top_header_filter = header_filter;
    next_body = ngx_http_top_body_filter; ngx_http_top_body_filter = body_filter;
    next_request_body = ngx_http_top_request_body_filter;
    if (next_request_body) ngx_http_top_request_body_filter = request_body_filter;
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
    ngx_core_conf_t *core = (ngx_core_conf_t *) ngx_get_conf(cycle->conf_ctx, ngx_core_module);
    ngx_event_conf_t *events = ngx_event_get_conf(cycle->conf_ctx, ngx_event_core_module);
    configured_workers = core->worker_processes;
    worker_connection_limit = events->connections;
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
