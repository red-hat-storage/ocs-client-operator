package console

var nginxConf = `
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

    # Load modular configuration files from the /etc/nginx/conf.d directory.
    # See http://nginx.org/en/docs/ngx_core_module.html#include
    # for more information.
    include /opt/app-root/etc/nginx.d/*.conf;

    server {
        listen       9001 ssl;
        listen       [::]:9001 ssl;
        ssl_certificate /var/serving-cert/tls.crt;
        ssl_certificate_key /var/serving-cert/tls.key;
        location / {
            root   /opt/app-root/src;
        }
        location /compatibility/ {
            root   /opt/app-root/src;
        }
        error_page   500 502 503 504  /50x.html;
        location = /50x.html {
            root   /usr/share/nginx/html;
        }
        ssi on;
        add_header Last-Modified $date_gmt;
        add_header Cache-Control 'no-store, no-cache, must-revalidate, proxy-revalidate, max-age=0';
        if_modified_since off;
        expires off;
        etag off;
    }

}
`
