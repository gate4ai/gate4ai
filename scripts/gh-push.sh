# Push в репозитории github.com/gate4ai/* от аккаунта gate4ai-alex: подключается через
# source (так делает deploy.sh). Токен берётся из gh и не сохраняется в git config.
# shellcheck shell=bash
export T=$(gh auth token --user gate4ai-alex)
gpush() { # gpush <repo> <аргументы git push>
  git -C "$1" -c credential.helper= -c 'credential.helper=!f() { echo username=x-access-token; echo "password=$T"; }; f' push "${@:2}"
}
