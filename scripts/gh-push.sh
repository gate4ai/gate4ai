# Push в репозитории github.com/gate4ai/* от аккаунта gate4ai-alex: подключается через
# source (так делает deploy.sh). Токен берётся из gh и не сохраняется в git config.
# shellcheck shell=bash
export T=$(gh auth token --user gate4ai-alex)
# ggit <repo> <аргументы git> — git с токеном: fetch приватных репозиториев (www) тоже
# требует его, активный аккаунт gh их не видит.
ggit() {
  git -C "$1" -c credential.helper= -c 'credential.helper=!f() { echo username=x-access-token; echo "password=$T"; }; f' "${@:2}"
}
gpush() { ggit "$1" push "${@:2}"; }  # gpush <repo> <аргументы git push>
gfetch() { ggit "$1" fetch "${@:2}"; } # gfetch <repo> <аргументы git fetch>
