#!/bin/sh
# Pinned pure-Python wheels. Installation requires unzip, not host Python/pip.
set -eu
python_dir=$1
cache=$2
site_packages="$python_dir/lib/python3.14/site-packages"
marker="$site_packages/.workflow-pydantic-1.10.26-typing-4.16.0"
[ ! -f "$marker" ] || exit 0
mkdir -p "$site_packages" "$cache/wheels"
install_wheel() {
    wheel="$cache/wheels/$1"
    url=$2
    expected=$3
    if [ ! -f "$wheel" ]; then
        curl -fL "$url" -o "$wheel.download"
        mv "$wheel.download" "$wheel"
    fi
    actual=$(shasum -a 256 "$wheel" | cut -d ' ' -f 1)
    [ "$actual" = "$expected" ] || { echo "Wheel checksum mismatch: $1" >&2; exit 1; }
    unzip -q -o "$wheel" -d "$site_packages"
}
install_wheel pydantic-1.10.26-py3-none-any.whl \
    https://files.pythonhosted.org/packages/1f/98/556e82f00b98486def0b8af85da95e69d2be7e367cf2431408e108bc3095/pydantic-1.10.26-py3-none-any.whl \
    c43ad70dc3ce7787543d563792426a16fd7895e14be4b194b5665e36459dd917
install_wheel typing_extensions-4.16.0-py3-none-any.whl \
    https://files.pythonhosted.org/packages/49/d3/b8441a820a491ddfc024b0b0cf0393375b75ea13866d9c66727e54c2fc80/typing_extensions-4.16.0-py3-none-any.whl \
    481caa481374e813c1b176ada14e97f1f67a4539ce9cfeb3f350d78d6370c2e8
touch "$marker"
