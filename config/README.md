## nginx
### メモ
stub status のエンドポイントは `:8888/status`

trace 等を送る先は `otel-collector-agent:14317`

nginx の image は `nginx:stable-otel`

access, error log が symbolic link に書き換えられないように、ファイル名を変えてる

### 参考
https://nginx.org/en/docs/ngx_otel_module.html

