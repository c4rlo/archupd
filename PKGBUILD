pkgname=archupd
pkgver=0.1.0
pkgrel=1
pkgdesc='Arch updater'
arch=('x86_64')
url="https://github.com/c4rlo/archupd"
license=('MIT')
makedepends=('go')
source=("$url/archive/refs/tags/$pkgver.tar.gz")
sha256sums=('1dbc8791c374e08e31d6a3baaa5d0233ab98a060ba9913b88b32c1aabce8b720')

build() {
  cd "$pkgname-$pkgver"
  go build -buildmode=pie -trimpath
}

package() {
  cd "$pkgname-$pkgver"
  install -D "$pkgname" "$pkgdir/usr/bin/$pkgname"
}
