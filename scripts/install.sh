#!/usr/bin/env bash
set -euo pipefail

project_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
package_path="github.com/Johnny-Cheung/Johnny-Code/cmd/johnny"

if ! command -v go >/dev/null 2>&1; then
  printf '安装需要 Go，请先安装项目要求的 Go 版本。\n' >&2
  exit 1
fi

bin_dir="$(go env GOBIN)"
if [[ -z "$bin_dir" ]]; then
  go_path="$(go env GOPATH)"
  bin_dir="${go_path%%:*}/bin"
fi
binary="$bin_dir/johnny"

if [[ -e "$binary" || -L "$binary" ]]; then
  if [[ -L "$binary" || ! -f "$binary" ]]; then
    printf '安装位置已有其他文件，未覆盖：%s\n' "$binary" >&2
    exit 1
  fi
  build_info="$(go version -m "$binary" 2>/dev/null || true)"
  if ! printf '%s\n' "$build_info" | grep -Fqx $'\tpath\t'"$package_path"; then
    printf '安装位置已有非本项目程序，未覆盖：%s\n' "$binary" >&2
    exit 1
  fi
fi

revision="unknown"
if command -v git >/dev/null 2>&1 && git -C "$project_root" rev-parse --verify HEAD >/dev/null 2>&1; then
  revision="$(git -C "$project_root" rev-parse --short HEAD)"
  if [[ -n "$(git -C "$project_root" status --porcelain --untracked-files=normal)" ]]; then
    revision="${revision}-dirty"
  fi
fi

# 路径先编码，避免空格等字符影响 Go 链接参数；不读取配置内容。
config_path_base64="$(printf '%s' "$project_root/.env" | base64 | tr -d '\n')"
ldflags="-X main.configFileBase64=$config_path_base64 -X main.commit=$revision"

printf '正在构建并安装 johnny…\n'
(
  cd -- "$project_root"
  go install -ldflags "$ldflags" ./cmd/johnny
)
printf '已安装：%s\n' "$binary"
"$binary" --version

user_shell="${SHELL:-}"
case "${user_shell##*/}" in
  zsh) shell_rcs=("${ZDOTDIR:-$HOME}/.zshrc") ;;
  bash)
    login_profile="$HOME/.bash_profile"
    for name in .bash_profile .bash_login .profile; do
      if [[ -f "$HOME/$name" ]]; then
        login_profile="$HOME/$name"
        break
      fi
    done
    shell_rcs=("$HOME/.bashrc" "$login_profile")
    ;;
  *)
    printf '请将以下目录加入当前 shell 的 PATH：%s\n' "$bin_dir" >&2
    exit 1
    ;;
esac

# Bash 的登录与非登录终端可能分别加载两个文件，运行时也避免重复添加目录。
printf -v path_line 'case ":$PATH:" in *:%q:*) ;; *) export PATH=%q:"$PATH" ;; esac' "$bin_dir" "$bin_dir"
for shell_rc in "${shell_rcs[@]}"; do
  if [[ -f "$shell_rc" ]] && grep -Fqx -- "$path_line" "$shell_rc"; then
    printf 'PATH 配置已存在：%s\n' "$shell_rc"
  else
    if ! printf '\n# Johnny-Code PATH\n%s\n' "$path_line" >> "$shell_rc"; then
      printf '程序已安装，但无法写入 PATH 配置：%s\n' "$shell_rc" >&2
      exit 1
    fi
    printf '已补充 PATH 配置：%s\n' "$shell_rc"
  fi
done
printf '请新开终端，或在当前终端加载上述 shell 配置文件后使用 johnny。\n'
