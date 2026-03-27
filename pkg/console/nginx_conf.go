package console

type NginxProxyConfData struct {
	ClientUID    string
	ExposeAs     string
	EndpointURL  string
	EndpointHost string
	CertsPath    string
}

var nginxProxyConf = `
location /{{.ClientUID}}/{{.ExposeAs}}/ {
    # Skip Origin/Referer checks (not guaranteed on all requests).

    # Operation check (data operations are not allowed).
    if ($is_op_allowed = 0) {
        return 403 "Not allowed: only management operations are whitelisted";
    }

    # Rate/connection limits.
    limit_req zone=proxy_req burst=50 nodelay;
    limit_conn proxy_conn 200;
    proxy_connect_timeout 60s;  # TLS handshake to backend
    proxy_send_timeout 60s;     # Send request to backend
    proxy_read_timeout 60s;     # Read response from backend

    proxy_pass {{.EndpointURL}}/;
    proxy_set_header Host {{.EndpointHost}};
    proxy_request_buffering off;
    proxy_buffering off;
    proxy_ssl_name {{.EndpointHost}};
    proxy_ssl_verify on;
    proxy_ssl_server_name on;
    proxy_ssl_trusted_certificate {{.CertsPath}};
}
`

var nginxRootConf = `
# Do not comment/un-comment without any reference.

worker_processes auto;
error_log /var/log/nginx/error.log;
pid /var/lib/nginx/tmp/nginx.pid;

# Load dynamic modules. See /usr/share/doc/nginx/README.dynamic.
include /usr/share/nginx/modules/*.conf;

events {
    worker_connections 1024;
}

http {
    # Use directories writable by unprivileged users.
    client_body_temp_path /var/lib/nginx/tmp/client_temp;
    proxy_temp_path       /var/lib/nginx/tmp/proxy_temp_path;
    fastcgi_temp_path     /var/lib/nginx/tmp/fastcgi_temp;
    uwsgi_temp_path       /var/lib/nginx/tmp/uwsgi_temp;
    scgi_temp_path        /var/lib/nginx/tmp/scgi_temp;

    log_format  main  '$remote_addr - $remote_user [$time_local] "$request" '
                      '$status $body_bytes_sent "$http_referer" '
                      '"$http_user_agent" "$http_x_forwarded_for"';

    access_log  /var/log/nginx/access.log  main;

    sendfile            on;
    tcp_nopush          on;
    tcp_nodelay         on;
    keepalive_timeout   65;
    types_hash_max_size 4096;

    include             /etc/nginx/mime.types;
    default_type        application/octet-stream;

    # Rate/connection limits global across all IPs (DDoS mitigation).
    # Separate zones for root (UI assets) vs proxy (external endpoints).
    limit_req_zone $server_name zone=root_req:1m rate=200r/s;
    limit_req_zone $server_name zone=proxy_req:1m rate=50r/s;
    limit_conn_zone $server_name zone=root_conn:1m;
    limit_conn_zone $server_name zone=proxy_conn:1m;

    # Only these operations are allowed on proxy locations.
    map "$request_method:$uri:$args" $is_op_allowed {
        default 0;

        # Service-level GET (ListBuckets)
        "~*^GET:/[^/]+/[^/]+/?:.*"    1;

        # Bucket-level GET
        "~*^GET:/[^/]+/[^/]+/[^/]+/?:.*list-type=2"    1; # ListObjectsV2
        "~*^GET:/[^/]+/[^/]+/[^/]+/?:.*versions"       1;
        "~*^GET:/[^/]+/[^/]+/[^/]+/?:.*acl"            1;
        "~*^GET:/[^/]+/[^/]+/[^/]+/?:.*cors"           1;
        "~*^GET:/[^/]+/[^/]+/[^/]+/?:.*encryption"     1;
        "~*^GET:/[^/]+/[^/]+/[^/]+/?:.*versioning"     1;
        "~*^GET:/[^/]+/[^/]+/[^/]+/?:.*lifecycle"      1;
        "~*^GET:/[^/]+/[^/]+/[^/]+/?:.*policy"         1;
        "~*^GET:/[^/]+/[^/]+/[^/]+/?:.*tagging"        1;
        "~*^GET:/[^/]+/[^/]+/[^/]+/?:.*policyStatus"   1;
        "~*^GET:/[^/]+/[^/]+/[^/]+/?:.*publicAccessBlock"  1;

        # Bucket-level PUT
        "~*^PUT:/[^/]+/[^/]+/[^/]+/?:$"                1; # CreateBucket
        "~*^PUT:/[^/]+/[^/]+/[^/]+/?:.*versioning"     1;
        "~*^PUT:/[^/]+/[^/]+/[^/]+/?:.*tagging"        1;
        "~*^PUT:/[^/]+/[^/]+/[^/]+/?:.*cors"           1;
        "~*^PUT:/[^/]+/[^/]+/[^/]+/?:.*lifecycle"      1;
        "~*^PUT:/[^/]+/[^/]+/[^/]+/?:.*policy"         1;
        "~*^PUT:/[^/]+/[^/]+/[^/]+/?:.*publicAccessBlock"  1;
        "~*^PUT:/[^/]+/[^/]+/[^/]+/?:.*encryption"     1;

        # Bucket-level DELETE
        "~*^DELETE:/[^/]+/[^/]+/[^/]+/?:$"             1; # DeleteBucket
        "~*^DELETE:/[^/]+/[^/]+/[^/]+/?:.*policy"      1;
        "~*^DELETE:/[^/]+/[^/]+/[^/]+/?:.*cors"        1;
        "~*^DELETE:/[^/]+/[^/]+/[^/]+/?:.*lifecycle"   1;

        # Bucket-level POST
        "~*^POST:/[^/]+/[^/]+/[^/]+/?:.*delete"        1; # DeleteObjects (bulk)

        # Object-level GET
        "~*^GET:/[^/]+/[^/]+/[^/]+/.+:.*tagging"     1;

        # Object-level DELETE
        "~*^DELETE:/[^/]+/[^/]+/[^/]+/.+:.*x-id=DeleteObject"          1;
        "~*^DELETE:/[^/]+/[^/]+/[^/]+/.+:.*x-id=AbortMultipartUpload"  1;

        # Bucket-level and Object-level HEAD
        "~*^HEAD:/[^/]+/[^/]+/[^/]+/?:?.*"            1;
        "~*^HEAD:/[^/]+/[^/]+/[^/]+/.+"               1;

        # Service-level, Bucket-level and Object-level OPTIONS (CORS preflight)
        "~*^OPTIONS:/[^/]+/[^/]+/?:?.*"               1;
        "~*^OPTIONS:/[^/]+/[^/]+/[^/]+/?:?.*"         1;
        "~*^OPTIONS:/[^/]+/[^/]+/[^/]+/.+"            1;
    }

    server {
        listen       9001 ssl;
        listen       [::]:9001 ssl;
        ssl_certificate /var/serving-cert/tls.crt;
        ssl_certificate_key /var/serving-cert/tls.key;

        location / {
            # Rate/connection limits.
            limit_req zone=root_req burst=100 nodelay;
            limit_conn root_conn 400;
            send_timeout 60s;  # Time to send response to client

            root   /opt/app-root/src;
            ssi on;
            add_header Last-Modified $date_gmt;
            add_header Cache-Control 'no-store, no-cache, must-revalidate, proxy-revalidate, max-age=0';
            if_modified_since off;
            expires off;
            etag off;
        }

        location /compatibility/ {
            # Rate/connection limits.
            limit_req zone=root_req burst=100 nodelay;
            limit_conn root_conn 400;
            send_timeout 60s; # Time to send response to client

            root   /opt/app-root/src;
            ssi on;
            add_header Last-Modified $date_gmt;
            add_header Cache-Control 'no-store, no-cache, must-revalidate, proxy-revalidate, max-age=0';
            if_modified_since off;
            expires off;
            etag off;
        }

        # Load modular configuration files from the "/opt/app-root/etc/nginx.d/" directory.
        # Proxy "location" directives are included here.
        # See http://nginx.org/en/docs/ngx_core_module.html#include
        # for more information.
        include /opt/app-root/etc/nginx.d/proxy-*.conf;

        error_page   500 502 503 504  /50x.html;
        location = /50x.html {
            root   /usr/share/nginx/html;
        }
    }
}
`
