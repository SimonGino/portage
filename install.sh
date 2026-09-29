#!/bin/sh
# portage 安装脚本：探平台 → 下载 GitHub Release 的 tar.gz → sha256 校验 → 放进安装目录。
# 只做这四步：不注册 systemd、不建用户、不建目录，结尾打印下一步。
#
#   curl -fsSL https://raw.githubusercontent.com/SimonGino/portage/main/install.sh | sh
#   curl -fsSL https://raw.githubusercontent.com/SimonGino/portage/main/install.sh | sh -s -- 0.1.1   # 钉版本
#
# 环境变量：
#   PORTAGE_VERSION        要装的版本（与第一个参数等价，参数优先）；缺省取最新 Release
#   PORTAGE_INSTALL_DIR    安装目录，默认 /usr/local/bin；不可写时脚本不提权，只提示
#   PORTAGE_DOWNLOAD_BASE  下载前缀，拼在完整 GitHub URL 之前（如 https://ghfast.top/）；
#                          经代理时 checksums.txt 与包同源同代理，校验防不了代理本身作恶
set -eu

repo="https://github.com/SimonGino/portage"
base="${PORTAGE_DOWNLOAD_BASE:-}"
dir="${PORTAGE_INSTALL_DIR:-/usr/local/bin}"
version="${1:-${PORTAGE_VERSION:-}}"
version="${version#v}"

die() {
	echo "portage 安装：$*" >&2
	exit 1
}

command -v curl >/dev/null 2>&1 || die "需要 curl"
command -v tar >/dev/null 2>&1 || die "需要 tar"

case "$(uname -s)" in
Linux) os=linux ;;
Darwin) os=darwin ;;
*) die "不支持的系统 $(uname -s)（只有 linux / darwin）" ;;
esac
case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
*) die "不支持的架构 $(uname -m)（只有 amd64 / arm64）" ;;
esac
[ "$os/$arch" = darwin/amd64 ] && die "不提供 darwin/amd64 产物"

# 先判安装目录，免得下完几十 MB 才发现放不进去。
if [ ! -d "$dir" ] || [ ! -w "$dir" ]; then
	die "$dir 不存在或不可写。以 root 重跑（curl … | sudo sh），或设 PORTAGE_INSTALL_DIR 指到可写目录"
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM

# 缺省版本不打 GitHub API（未鉴权 60 次/时），从 latest 的 checksums.txt 文件名里解析。
if [ -z "$version" ]; then
	sums_url="${base}${repo}/releases/latest/download/checksums.txt"
else
	sums_url="${base}${repo}/releases/download/v${version}/checksums.txt"
fi
curl -fsSL -o "$tmp/checksums.txt" "$sums_url" || die "下载失败：$sums_url"

line="$(grep "  portage_[^ ]*_${os}_${arch}\.tar\.gz\$" "$tmp/checksums.txt" | head -n 1 || true)"
[ -n "$line" ] || die "checksums.txt 里没有 ${os}/${arch} 的条目"
asset="${line##* }"
if [ -z "$version" ]; then
	version="${asset#portage_}"
	version="${version%_"${os}"_"${arch}".tar.gz}"
fi
[ "$asset" = "portage_${version}_${os}_${arch}.tar.gz" ] || die "checksums.txt 里没有 ${asset} 对应的条目"

echo "下载 portage ${version}（${os}/${arch}）"
pkg_url="${base}${repo}/releases/download/v${version}/${asset}"
curl -fSL --progress-bar -o "$tmp/$asset" "$pkg_url" || die "下载失败：$pkg_url"

echo "$line" >"$tmp/sum.txt"
if command -v sha256sum >/dev/null 2>&1; then
	(cd "$tmp" && sha256sum -c sum.txt >/dev/null) || die "sha256 校验不符，已丢弃"
elif command -v shasum >/dev/null 2>&1; then
	(cd "$tmp" && shasum -a 256 -c sum.txt >/dev/null) || die "sha256 校验不符，已丢弃"
else
	die "需要 sha256sum 或 shasum 做校验"
fi

# 只解包里那一个 portage，别的条目一概不落地。
tar -xzf "$tmp/$asset" -C "$tmp" portage || die "解包失败"

# 先落同目录临时名再 mv：mv 是 rename，覆盖一个正在运行的 portage 不会撞 ETXTBSY。
cp "$tmp/portage" "$dir/.portage.new"
chmod 755 "$dir/.portage.new"
mv -f "$dir/.portage.new" "$dir/portage"

echo "已安装：$("$dir/portage" -version)  →  $dir/portage"
cat <<NEXT

下一步（脚本不替你做）：
  1. 配置：参照 ${repo}/blob/main/deploy/config.example.yaml 写 /etc/portage/config.yaml
  2. 常驻：参照 ${repo}/blob/main/deploy/portage.service 装 systemd unit，
     然后 systemctl daemon-reload && systemctl enable --now portage
NEXT
