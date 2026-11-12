<div align="center">

<img src="docs/logo.png" width="120" alt="葵码生成器">

# 葵码生成器

在线生成与解析微信葵花码（太阳码），支持中心 Logo、动图、水印与批量生成。

<!-- 版本号与仓库根目录的 VERSION 文件保持一致 -->
[![在线演示](https://img.shields.io/badge/%E5%9C%A8%E7%BA%BF%E6%BC%94%E7%A4%BA-wx.xbao.fun-2F6BFF?logo=googlechrome&logoColor=white)](https://wx.xbao.fun)
[![Version](https://img.shields.io/badge/version-1.1.0-07C160)](https://github.com/AnglerS888/kuima-generator/releases)
[![Gitee](https://img.shields.io/badge/Gitee-anglers%2Fkuima--generator-C71D23?logo=gitee&logoColor=white)](https://gitee.com/anglers/kuima-generator)
[![GitHub](https://img.shields.io/badge/GitHub-AnglerS888%2Fkuima--generator-181717?logo=github&logoColor=white)](https://github.com/AnglerS888/kuima-generator)
![Go](https://img.shields.io/badge/Go-1.22+-00ADD8?logo=go&logoColor=white)
![Docker](https://img.shields.io/badge/Docker-%E6%94%AF%E6%8C%81-2496ED?logo=docker&logoColor=white)

[在线演示](https://wx.xbao.fun) · [功能](#功能) · [界面预览](#界面预览) · [安装](#安装) · [怎么用](#怎么用) · [在线更新](#在线更新) · [常见问题](#常见问题) · [关于作者](#关于作者)

<img src="docs/example.gif" width="420" alt="生成演示">

<sub>输入链接 → 选中心 Logo → 生成并下载</sub>

</div>

## 在线演示

先看看效果：**<https://wx.xbao.fun>**（前台可直接生成；管理后台不对外开放，可看下方截图或自己部署）。

## 功能

- **生成葵花码**：输入网址或文字，输出 PNG / JPG / SVG / GIF，尺寸、留白、颜色、纠错等级可调。
- **笔画风格**：经典 / 手绘 / 圆点 / 方块 / 线描五种画法，抖动与变形都控制在半格内，不影响扫码；手绘风格下角落徽标也会一起"手贴"歪。
- **配色**（与笔画风格自由组合）：纯色（用前景色）/ 色环（按半径渐变）/ 彩虹（每一笔随机一色）/ **隐形**（墨色只比底色深 2~40 级，肉眼几乎看不出，程序与手机仍可识别；深度可调，越小越隐蔽）。
- **中心 Logo**：上传 PNG / JPG / GIF，GIF 动图能生成会动的码；后台可维护一批预设 Logo，用户一键选用。
- **徽标与文字水印**：角落徽标可开关或上传 PNG / JPG / GIF，支持恢复默认；中心 Logo 与徽标可同时播放 GIF 动画，前端自动切换为 GIF 输出，合成最多 20 帧（超出时按完整周期抽帧）；关闭徽标后可开启「填补徽标空位」，用同风格码点补齐右下角空白，纹理由输入内容决定，相同内容保持一致；水印支持多位置、平铺 / 斜铺，颜色、透明度可调。
- **解析**：截图、拍照、轻微旋转、带 Logo 的码都能识别。
- **好友码 / 批量**：上传微信二维码名片截图生成好友码；一次提交多条批量出图。
- **账号与后台**：注册、登录、邀请码、卡密、额度、调用记录、数据备份，一个管理后台全搞定。

## 界面预览

**前台 · 生成页**

<img src="docs/screenshot-home.png" width="860" alt="生成页">

**后台 · 概览**

<img src="docs/screenshot-admin.png" width="860" alt="管理后台概览">

**后台 · 预设 Logo**

<img src="docs/screenshot-preset.png" width="860" alt="后台预设 Logo">

**笔画风格 × 配色（第一行 经典 / 手绘 / 圆点，第二行 方块 / 线描 / 经典·彩虹，第三行 经典·色环 / 圆点·彩虹 / 手绘·色环）**

<img src="docs/style-samples.png" width="860" alt="笔画风格一览">

## 安装

三种方式任选其一，装好后访问 `http://服务器IP:8080/`，管理后台在 `/admin`。
首次启动会创建管理员：设置了 `ADMIN_PASSWORD` 就用它，没设置会随机生成并打印在启动日志里。

### 方式一：一键部署（Linux + Docker，推荐）

按服务器所在网络选一条命令执行，脚本会自动拉源码、构建镜像、启动容器，并打印管理员账号密码：

```bash
# 国外服务器
curl -fsSL https://github.com/AnglerS888/kuima-generator/raw/main/bin/deploy.sh -o deploy.sh
bash deploy.sh

# 国内服务器
curl -fsSL https://raw.giteeusercontent.com/anglers/kuima-generator/raw/main/bin/deploy.sh -o deploy.sh
bash deploy.sh
```

再执行一次就是升级。常用参数：

```bash
PORT=9090 bash deploy.sh     # 换端口
bash deploy.sh --status      # 查看状态和管理员密码
bash deploy.sh --rollback    # 回滚到上一版
```

### 方式二：Docker Compose

```bash
git clone https://gitee.com/anglers/kuima-generator.git   # 国外用 github.com/AnglerS888/kuima-generator.git
cd kuima-generator
docker compose up -d --build
docker compose logs -f wxcode     # 这里能看到随机生成的管理员密码
```

### 方式三：直接运行

需要 Go 1.22+。编译出一个可执行文件即可，数据存本地 SQLite，不用装数据库。

```bash
# Linux
CGO_ENABLED=0 go build -ldflags "-X wxcode/internal/config.Version=$(cat VERSION)" -o wxcode ./cmd/wxcode
PORT=8080 DATA_DIR=./data ADMIN_USER=admin ADMIN_PASSWORD='你的密码' ./wxcode
```

Windows 用 PowerShell：

```powershell
go build -ldflags "-X wxcode/internal/config.Version=$(cat VERSION)" -o wxcode.exe ./cmd/wxcode
$env:PORT="8080"; $env:ADMIN_PASSWORD="你的密码"; .\wxcode.exe
```

Windows 也可以直接双击 `wxcode.exe`：默认 8080 端口、数据在程序同级 `data` 目录，管理员密码打印在窗口里。

## 怎么用

1. **生成**：打开首页 → 填内容（网址或文字）→ 选格式和尺寸，可选上传中心 Logo、调颜色 → 点生成并下载。
2. **解析**：切到「解析」页 → 上传截图或拍照 → 得到码里的内容和码型信息。
3. **好友码**：上传微信「二维码名片」截图 → 生成对应的好友太阳码。
4. **批量**：一次填多行内容，批量生成后打包下载。
5. **后台**：`/admin` 里可以管用户与额度、预设 Logo、公告、水印、邀请码 / 卡密、备份导入导出。
6. **接口**：首页「接口」页或 `/api/docs.md` 有在线文档，可直接复制 curl 示例。

## 常用配置

| 变量 | 说明 |
|---|---|
| `PORT` | 监听端口，默认 `8080` |
| `DATA_DIR` | 数据目录，默认 `./data`（数据库文件在这里） |
| `ADMIN_USER` / `ADMIN_PASSWORD` | 首次启动创建的管理员账号 / 密码 |
| `ADMIN_RESET_PASSWORD=1` | 忘记密码时，配合 `ADMIN_PASSWORD` 重启一次即可重置 |
| `API_ONLY=1` | 启用纯 API 模式，默认关闭 |
| `API_CORS_ORIGINS` | 纯 API 模式的跨域来源白名单，逗号分隔；`*` 允许所有来源，默认不允许跨域 |

### 纯 API 模式

需要生成网页和接口，但不需要账号与管理后台时，可设置 `API_ONLY=1` 启动：

```bash
API_ONLY=1 PORT=8080 ./wxcode
# Docker Compose（使用仓库自带的 compose 文件）
API_ONLY=1 docker compose up -d --build
```

此模式保留首页 `/`，可在网页生成、解析和使用接口调试台，支持自定义水印。接口包括 `/api/encode`、`/api/encode/batch`、`/api/decode`、`/api/qr`、生成结果的临时图片地址 `/api/code/{name}`，以及 `/api/health`、`/api/docs.md`、`/api/openapi.json`、`/api/postman.json`。账号和管理相关的页面及接口不可用。接口无需登录或 API Key，不设账号额度，仍有请求限速和并发保护；如需限制调用者，请在反向代理或 API 网关配置鉴权。默认不允许浏览器跨域，可通过 `API_CORS_ORIGINS=https://example.com` 配置。

纯 API 模式使用进程内临时数据库，不创建管理员、不写调用记录，也不读取或修改原有的数据目录；重启后临时图片地址失效。移除 `API_ONLY` 或设为 `0` 后，以原有数据恢复普通模式。

## 在线更新

后台「概览 → 版本与更新」：

- 有新版本时页面会直接提示，点「更新到 vX」即可，**升级进度和报错都会显示在页面上**，完成后自动刷新。
- 版本号没变、但想重新拉一遍远端程序时，点「强制更新」。
- 用 Docker 部署的话，建议还是用 `bash deploy.sh` 升级（重建镜像更干净）。

## 常见问题

**扫出来只显示文字，不跳转？**
葵花码承载的是内容本身，微信只对 `http(s)` 开头的网址跳转；要扫码打开小程序，需要用微信后台签发的小程序码。

**数据在哪？怎么备份或迁移？**
`DATA_DIR` 下的 `wxcode.db`。停机后拷走这个文件（或整个 `data` 目录），放到新机器同样的位置启动即可，账号、额度、记录、预设 Logo 都在。后台也能直接导出 / 导入。

**忘记管理员密码？**
带 `ADMIN_RESET_PASSWORD=1` 和新的 `ADMIN_PASSWORD` 重启一次。

## 关于作者

- **作者微信**：`KFC_good` —— 使用问题、部署求助、功能定制都可以加，备注一下「葵码」即可。
- 如果这个项目帮到了你，欢迎请作者喝杯咖啡：

<div align="center">

<img src="docs/reward.jpg" width="300" alt="赞赏码">

<sub>Anglers 的赞赏码</sub>

</div>

---

本项目与腾讯、微信无任何关联，仅供学习研究与内部使用。
