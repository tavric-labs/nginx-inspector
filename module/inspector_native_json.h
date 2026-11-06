/* Native state serialization. Included after the bounded JSON writer helpers. */
static void key(writer *w, const char *name) {
    literal(w, ",\""); literal(w, name); literal(w, "\":");
}
static void integer_field(writer *w, const char *name, int64_t value) {
    key(w, name); number(w, value);
}
static void flag_field(writer *w, const char *name, ngx_flag_t value) {
    key(w, name); literal(w, value ? "true" : "false");
}
static void str_field(writer *w, const char *name, ngx_str_t value) {
    key(w, name); string(w, value.data, value.len);
}
static void time_value(writer *w, ngx_msec_t value) {
    if (value == (ngx_msec_t) -1) literal(w, "null"); else number(w, value);
}
static void nullable_size(writer *w, off_t value) {
    if (value < 0) literal(w, "null"); else number(w, value);
}
static const char *location_kind(ngx_http_core_loc_conf_t *location) {
    if (location->named) return "named";
#if (NGX_PCRE)
    if (location->regex) return "regex";
#endif
    return location->exact_match ? "exact" : "prefix";
}
static const char *buffering_hint(ngx_http_upstream_t *upstream) {
    ngx_list_part_t *part = &upstream->headers_in.headers.part;
    ngx_table_elt_t *headers;
    ngx_uint_t i, visited = 0;
    while (part && visited < 128) {
        headers = part->elts;
        for (i = 0; i < part->nelts && visited < 128; i++, visited++) {
            if (headers[i].key.len != sizeof("X-Accel-Buffering") - 1
                || ngx_strncasecmp(headers[i].key.data, (u_char *) "X-Accel-Buffering", sizeof("X-Accel-Buffering") - 1)) continue;
            if (headers[i].value.len == 2 && ngx_strncasecmp(headers[i].value.data, (u_char *) "no", 2) == 0) return "no";
            if (headers[i].value.len == 3 && ngx_strncasecmp(headers[i].value.data, (u_char *) "yes", 3) == 0) return "yes";
            return "other";
        }
        part = part->next;
    }
    return NULL;
}
static void retry_names(writer *w, ngx_uint_t mask) {
    static const struct { ngx_uint_t flag; const char *name; } flags[] = {
        {NGX_HTTP_UPSTREAM_FT_ERROR, "error"}, {NGX_HTTP_UPSTREAM_FT_TIMEOUT, "timeout"},
        {NGX_HTTP_UPSTREAM_FT_INVALID_HEADER, "invalid_header"},
        {NGX_HTTP_UPSTREAM_FT_HTTP_500, "http_500"}, {NGX_HTTP_UPSTREAM_FT_HTTP_502, "http_502"},
        {NGX_HTTP_UPSTREAM_FT_HTTP_503, "http_503"}, {NGX_HTTP_UPSTREAM_FT_HTTP_504, "http_504"},
        {NGX_HTTP_UPSTREAM_FT_HTTP_403, "http_403"}, {NGX_HTTP_UPSTREAM_FT_HTTP_404, "http_404"},
        {NGX_HTTP_UPSTREAM_FT_HTTP_429, "http_429"}, {NGX_HTTP_UPSTREAM_FT_NON_IDEMPOTENT, "non_idempotent"},
        {NGX_HTTP_UPSTREAM_FT_OFF, "off"}
    };
    ngx_uint_t i, first = 1;
    literal(w, "[");
    for (i = 0; i < sizeof(flags) / sizeof(flags[0]); i++) if (mask & flags[i].flag) {
        if (!first) literal(w, ",");
        first = 0;
        string(w, (u_char *) flags[i].name, ngx_strlen(flags[i].name));
    }
    literal(w, "]");
}
static void native_json(writer *w, ngx_http_request_t *r, inspector_ctx *c) {
    ngx_http_core_loc_conf_t *location = ngx_http_get_module_loc_conf(r, ngx_http_core_module);
    ngx_http_upstream_t *upstream = r->upstream;
    ngx_http_upstream_conf_t *policy = upstream ? upstream->conf : NULL;
    ngx_http_upstream_state_t *states;
    ngx_pool_t *pool;
    ngx_pool_large_t *large;
    ngx_uint_t i, j, group = 0, emitted = 0, omitted = 0, unscanned = 0, blocks = 0, large_count = 0;
    ngx_uint_t scan_limited = 0;
    size_t reserved = 0, used = 0;
    ngx_str_t value;
    const char *hint;
    ngx_http_request_t *sub;

    literal(w, ",\"native\":{\"nginx\":{\"version\":\"" NGINX_VERSION "\"");
    integer_field(w, "worker_pid", ngx_pid); integer_field(w, "master_pid", ngx_parent);
    integer_field(w, "configured_workers", configured_workers);
    integer_field(w, "worker_connections", worker_connection_limit);
    key(w, "event_backend"); literal(w, (ngx_event_flags & NGX_USE_EPOLL_EVENT) ? "\"epoll\"" : "\"other\"");
    flag_field(w, "accept_mutex", ngx_use_accept_mutex);
    integer_field(w, "accept_disabled", ngx_accept_disabled);
#if (NGX_STAT_STUB)
    integer_field(w, "connections_active", *ngx_stat_active);
    integer_field(w, "connections_reading", *ngx_stat_reading);
    integer_field(w, "connections_writing", *ngx_stat_writing);
    integer_field(w, "connections_waiting", *ngx_stat_waiting);
    integer_field(w, "connections_accepted", *ngx_stat_accepted);
    integer_field(w, "connections_handled", *ngx_stat_handled);
    integer_field(w, "requests_total", *ngx_stat_requests);
#endif
    for (pool = r->pool; pool && blocks < 32; pool = pool->d.next) {
        reserved += pool->d.end - (u_char *) pool;
        used += pool->d.last - (u_char *) pool; blocks++;
    }
    if (pool) scan_limited = 1;
    for (large = r->pool->large, i = 0; large && i < 32; large = large->next, i++)
        if (large->alloc) large_count++;
    if (large) scan_limited = 1;
    integer_field(w, "pool_reserved_bytes", reserved); integer_field(w, "pool_used_bytes", used);
    integer_field(w, "pool_blocks_counted", blocks); integer_field(w, "large_allocations_counted", large_count);
    literal(w, "},\"routing\":{\"original_path\":"); string(w, c->original_path.data, c->original_path.len);
    str_field(w, "initial_path", c->initial_path); str_field(w, "location", location->name);
    key(w, "location_kind"); string(w, (u_char *) location_kind(location), ngx_strlen(location_kind(location)));
    key(w, "server_name"); variable(w, r, "server_name");
    flag_field(w, "internal", r->internal); flag_field(w, "internal_location", location->internal);
    flag_field(w, "regex_disabled", location->noregex);
    literal(w, "},\"connection\":{\"id\":"); number(w, r->connection->number);
    integer_field(w, "request_number", c->connection_requests);
    key(w, "protocol"); variable(w, r, "server_protocol");
    flag_field(w, "keepalive", r->keepalive); flag_field(w, "pipeline", r->pipeline);
    flag_field(w, "client_error", r->connection->error); flag_field(w, "client_timedout", r->connection->timedout);
    key(w, "tls_session_reused"); variable(w, r, "ssl_session_reused");
    literal(w, "},\"request_body\":{\"filter_calls\":"); number(w, c->request_body_calls);
    integer_field(w, "buffers", c->request_body_buffers); integer_field(w, "observed_bytes", c->request_body_bytes);
    flag_field(w, "filter_available", next_request_body != NULL);
    flag_field(w, "disk_buffered", r->request_body && r->request_body->temp_file);
    integer_field(w, "temp_file_offset", r->request_body && r->request_body->temp_file ? r->request_body->temp_file->offset : 0);
    flag_field(w, "no_buffering", r->request_body_no_buffering);
    flag_field(w, "chunked", r->headers_in.chunked);
    key(w, "declared_bytes"); nullable_size(w, c->declared_body_bytes);
    integer_field(w, "buffer_limit_bytes", location->client_body_buffer_size);
    integer_field(w, "max_body_bytes", location->client_max_body_size);
    key(w, "first_filter_ms"); phase(w, c->request_body_first, c->start_mono);
    key(w, "last_filter_ms"); phase(w, c->request_body_last, c->start_mono);
    literal(w, "},\"response\":{\"filter_calls\":"); number(w, c->body_calls);
    integer_field(w, "buffers", c->body_buffers); integer_field(w, "memory_bytes", c->memory_bytes);
    integer_field(w, "file_bytes", c->file_bytes); integer_field(w, "flush_markers", c->body_flushes);
    integer_field(w, "sync_markers", c->body_syncs); integer_field(w, "last_filter_result", c->body_result);
    integer_field(w, "header_filter_result", c->header_result);
    integer_field(w, "request_buffered_mask", c->request_buffered_seen);
    integer_field(w, "connection_buffered_mask", c->connection_buffered_seen);
    integer_field(w, "upstream_temp_file_offset", c->temp_file_offset);
    flag_field(w, "header_only", r->header_only); flag_field(w, "chunked", r->chunked);
    flag_field(w, "range_allowed", r->allow_ranges);
    flag_field(w, "range_requested", r->headers_in.range != NULL);
    flag_field(w, "conditional_request", r->headers_in.if_modified_since != NULL || r->headers_in.if_none_match != NULL);
    flag_field(w, "sendfile_allowed", r->connection->sendfile);
    flag_field(w, "upstream_cached_peer_observed", c->cached_peer_seen);
    flag_field(w, "upstream_keepalive", upstream && upstream->keepalive);
    flag_field(w, "upstream_error", upstream && upstream->error);
    flag_field(w, "upstream_request_sent", upstream && upstream->request_sent);
    flag_field(w, "upstream_body_sent", upstream && upstream->request_body_sent);
    flag_field(w, "upstream_upgrade", upstream && upstream->upgrade);
#if (NGX_HTTP_CACHE)
    flag_field(w, "cache_file_used", r->cached);
#endif
    key(w, "declared_bytes"); nullable_size(w, r->headers_out.content_length_n);
    key(w, "content_encoding"); variable(w, r, "sent_http_content_encoding");
    key(w, "cache_control"); variable(w, r, "sent_http_cache_control");
    key(w, "gzip_ratio");
    optional_variable(w, r, gzip_ratio_index);
    key(w, "last_filter_ms"); phase(w, c->body_last, c->start_mono);
    literal(w, "},\"policy\":{\"upstream_available\":"); literal(w, policy ? "true" : "false");
    integer_field(w, "client_body_timeout_ms", location->client_body_timeout);
    integer_field(w, "client_send_timeout_ms", location->send_timeout);
    integer_field(w, "client_keepalive_timeout_ms", location->keepalive_timeout);
    key(w, "limit_rate_bps"); if (r->limit_rate_set) number(w, r->limit_rate); else literal(w, "null");
    key(w, "limit_rate_after_bytes"); if (r->limit_rate_after_set) number(w, r->limit_rate_after); else literal(w, "null");
    key(w, "rate_limit_status"); optional_variable(w, r, limit_req_index);
    key(w, "connection_limit_status"); optional_variable(w, r, limit_conn_index);
    key(w, "access_satisfy"); literal(w, location->satisfy == NGX_HTTP_SATISFY_ANY ? "\"any\"" : "\"all\"");
    key(w, "aio_mode"); literal(w, location->aio == NGX_HTTP_AIO_THREADS ? "\"threads\"" : location->aio == NGX_HTTP_AIO_ON ? "\"on\"" : "\"off\"");
    flag_field(w, "tcp_nopush", location->tcp_nopush);
    flag_field(w, "tcp_nodelay", location->tcp_nodelay);
    integer_field(w, "sendfile_max_chunk_bytes", location->sendfile_max_chunk);
    integer_field(w, "postpone_output_bytes", location->postpone_output);
    integer_field(w, "resolver_timeout_ms", location->resolver_timeout);
    integer_field(w, "lingering_timeout_ms", location->lingering_timeout);
    integer_field(w, "lingering_time_ms", location->lingering_time);
    if (policy) {
        flag_field(w, "request_buffering", policy->request_buffering);
        flag_field(w, "response_buffering_configured", policy->buffering);
        flag_field(w, "response_buffering", upstream->buffering);
        flag_field(w, "buffering_header_ignored", policy->ignore_headers & NGX_HTTP_UPSTREAM_IGN_XA_BUFFERING);
        hint = buffering_hint(upstream); key(w, "buffering_header");
        if (hint) string(w, (u_char *) hint, ngx_strlen(hint)); else literal(w, "null");
        integer_field(w, "connect_timeout_ms", policy->connect_timeout);
        integer_field(w, "read_timeout_ms", policy->read_timeout);
        integer_field(w, "send_timeout_ms", policy->send_timeout);
        integer_field(w, "header_buffer_bytes", policy->buffer_size);
        integer_field(w, "buffer_count", policy->bufs.num); integer_field(w, "buffer_size_bytes", policy->bufs.size);
        integer_field(w, "busy_buffers_bytes", policy->busy_buffers_size);
        integer_field(w, "max_temp_file_bytes", policy->max_temp_file_size);
        integer_field(w, "retry_timeout_ms", policy->next_upstream_timeout);
        integer_field(w, "retry_tries", policy->next_upstream_tries);
        key(w, "retry_on"); retry_names(w, policy->next_upstream);
#if (NGX_HTTP_CACHE)
        flag_field(w, "cache_configured", policy->cache > 0);
        flag_field(w, "cache_lock", policy->cache_lock);
        flag_field(w, "cache_revalidate", policy->cache_revalidate);
#endif
        if (upstream->upstream) str_field(w, "upstream_group", upstream->upstream->host);
    }
    literal(w, "},\"trace\":[");
    for (i = 0; i < c->trace_count; i++) {
        inspector_observation *o = &c->trace[i];
        if (i) literal(w, ",");
        literal(w, "{\"event\":"); value.data = (u_char *) observation_names[o->event]; value.len = ngx_strlen(value.data);
        string(w, value.data, value.len); integer_field(w, "at_ms", o->at - c->start_mono);
        integer_field(w, "subrequest", o->subrequest); integer_field(w, "io", o->io);
        integer_field(w, "status", o->status);
        integer_field(w, "request_buffered", o->request_buffered);
        integer_field(w, "connection_buffered", o->connection_buffered);
        if (o->event == OBS_BODY_STATE || o->event == OBS_REQUEST_BODY_RETURN || o->event == OBS_HEADERS_RETURN) integer_field(w, "result", o->result);
        if (o->path.data) str_field(w, "path", o->path);
        if (o->location.data) str_field(w, "location", o->location);
        literal(w, "}");
    }
    literal(w, "],\"attempts\":[");
    if (r->upstream_states) {
        states = r->upstream_states->elts;
        for (i = 0; i < r->upstream_states->nelts && i < 64; i++) {
            if (states[i].peer == NULL) { group++; continue; }
            if (emitted == INSPECTOR_ATTEMPT_MAX) { omitted++; continue; }
            if (emitted) literal(w, ",");
            emitted++;
            literal(w, "{\"peer\":"); string(w, states[i].peer->data, states[i].peer->len);
            integer_field(w, "group", group); integer_field(w, "status", states[i].status);
            key(w, "connect_ms"); time_value(w, states[i].connect_time);
            key(w, "header_ms"); time_value(w, states[i].header_time);
            key(w, "response_ms"); time_value(w, states[i].response_time);
            integer_field(w, "response_bytes", states[i].response_length);
            integer_field(w, "received_bytes", states[i].bytes_received);
            integer_field(w, "sent_bytes", states[i].bytes_sent); literal(w, "}");
        }
        if (i < r->upstream_states->nelts) unscanned = r->upstream_states->nelts - i;
    }
    literal(w, "],\"subrequests\":[");
    for (j = 0; j < c->sub_count; j++) {
        inspector_subrequest *s = &c->subs[j]; sub = s->request;
        if (j) literal(w, ",");
        literal(w, "{\"index\":"); number(w, j + 1); str_field(w, "initial_path", s->initial_path);
        str_field(w, "path", sub->uri);
        integer_field(w, "status", sub->headers_out.status ? sub->headers_out.status : sub->err_status);
        key(w, "first_ms"); phase(w, s->first, c->start_mono);
        key(w, "last_observed_ms"); phase(w, s->last, c->start_mono);
        key(w, "header_ms"); phase(w, s->header, c->start_mono);
        integer_field(w, "body_bytes", s->body_bytes); literal(w, "}");
    }
    literal(w, "],\"limits\":{\"trace_cap\":"); number(w, INSPECTOR_TRACE_MAX);
    integer_field(w, "trace_omitted", c->trace_omitted); integer_field(w, "attempts_cap", INSPECTOR_ATTEMPT_MAX);
    integer_field(w, "attempts_omitted", omitted); integer_field(w, "upstream_states_unscanned", unscanned);
    integer_field(w, "subrequests_cap", INSPECTOR_SUB_MAX);
    integer_field(w, "subrequest_observations_omitted", c->sub_omitted);
    integer_field(w, "payload_cap_bytes", INSPECTOR_PAYLOAD_MAX);
    integer_field(w, "string_cap_bytes", 256); integer_field(w, "trace_path_cap_bytes", 128);
    integer_field(w, "sample_permyriad", c->sample); flag_field(w, "pool_scan_limited", scan_limited);
    literal(w, "}}");
}
