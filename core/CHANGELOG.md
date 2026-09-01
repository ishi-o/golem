# Changelog

## [1.1.0](https://github.com/ishi-o/golem/compare/core/v1.0.0...core/v1.1.0) (2026-09-01)


### Features

* **deps:** bump core into v1.0.0 ([3ce47da](https://github.com/ishi-o/golem/commit/3ce47dae099f9062994eac7a89e1fbbbfc5b69fb))

## [1.0.0](https://github.com/ishi-o/golem/compare/core/v0.2.1...core/v1.0.0) (2026-09-01)


### ⚠ BREAKING CHANGES

* drop the app/cmd/internal modules, pin go 1.21

### Features

* add store impls ([#1](https://github.com/ishi-o/golem/issues/1)) ([b6fcf22](https://github.com/ishi-o/golem/commit/b6fcf2215b508fdbbb86abebc7e85308b10e5351))
* **core:** align facade boundaries and add connectors ([#25](https://github.com/ishi-o/golem/issues/25)) ([6d24eb3](https://github.com/ishi-o/golem/commit/6d24eb361d8c5af3ec4aed9812275d8bc59cdc20))
* **core:** extend Golem with Eino-native agent capabilities ([#24](https://github.com/ishi-o/golem/issues/24)) ([c168d9f](https://github.com/ishi-o/golem/commit/c168d9ff27a1c9f9386614bf41d6f88e51076201))
* drop the app/cmd/internal modules, pin go 1.21 ([a719bae](https://github.com/ishi-o/golem/commit/a719bae7fcbec32c1d40d99a82baf0729de591ed))
* port reasoning streaming, subagents, mid-run queueing and group/tenant scoping ([0ec834d](https://github.com/ishi-o/golem/commit/0ec834de6c1d5806fac29c320f663f9c384ab833))


### Bug Fixes

* **core:** drop inflated indirect requires, restore go 1.18 ([8be4285](https://github.com/ishi-o/golem/commit/8be4285d3695764205d9fe72a8736cf64e4dd776))
