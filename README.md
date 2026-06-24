# geoip

自定义 GeoIP，基于 MaxMind GeoLite2，每日自动更新，发布在 [`release`](https://github.com/duakc/geoip/tree/release) 分支。

包含所有国家（ISO 3166-1 代码，小写如 `cn`、`us`、`jp`），并在此基础上额外覆写/新增了下方「自定义」中的代码。

## 下载

GitHub 直连在国内可能不稳定，建议优先使用 jsDelivr。

合并 mmdb（单文件，包含全部代码）：

- GitHub：<https://raw.githubusercontent.com/duakc/geoip/release/mmdb/geoip.mmdb>
- jsDelivr：<https://cdn.jsdelivr.net/gh/duakc/geoip@release/mmdb/geoip.mmdb>

其它格式按代码取用，把 `<code>` 替换为目标代码（如 `cn`、`us`）：

| 格式 | jsDelivr | 说明 |
| --- | --- | --- |
| txt | `https://cdn.jsdelivr.net/gh/duakc/geoip@release/<code>` | 纯文本 CIDR 列表 |
| srs | `https://cdn.jsdelivr.net/gh/duakc/geoip@release/srs/<code>.srs` | sing-box（另有 `srs/<code>.json`） |
| mrs | `https://cdn.jsdelivr.net/gh/duakc/geoip@release/mrs/<code>.mrs` | mihomo（另有 `.list` / `.yaml` / `.classical`） |

> GitHub 直连把 `cdn.jsdelivr.net/gh/duakc/geoip@release` 换成 `raw.githubusercontent.com/duakc/geoip/release` 即可。例：中国移动 srs → `https://cdn.jsdelivr.net/gh/duakc/geoip@release/srs/cn-cm.srs`

## 自定义

以下代码由本仓库额外覆写或新增，优先级高于 MaxMind：

| 代码 | 说明 |
| --- | --- |
| `cn` | 中国大陆（覆写 MaxMind） |
| `cn-cm` | 中国移动 |
| `cn-ct` | 中国电信 |
| `cn-cu` | 中国联通 |

## 致谢

- [MaxMind GeoLite2](https://www.maxmind.com)
- [misakaio/chnroutes2](https://github.com/misakaio/chnroutes2)
- [gaoyifan/china-operator-ip](https://github.com/gaoyifan/china-operator-ip)

## License

[MIT](LICENSE)