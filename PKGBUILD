pkgname=archupd
pkgver=0.1.0
pkgrel=1
pkgdesc='Arch updater'
arch=('x86_64')
url="https://github.com/c4rlo/archupd"
license=('MIT')
makedepends=('go')
source=("$url/archive/refs/tags/$pkgver.tar.gz")
sha256sums=('a61561256482c9824e78424a1b42342efcaf5baee8638adc15323352e37054b3')

build() {
  cd "$pkgname-$pkgver"
  go build -buildmode=pie -trimpath
}

package() {
  cd "$pkgname-$pkgver"
  install -D "$pkgname" "$pkgdir/usr/bin/$pkgname"
}
