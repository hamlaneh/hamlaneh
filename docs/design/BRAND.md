# Hamlaneh brand assets

The files this describes live in `webapp/public/brand/`. This note deliberately does
not, because everything under `webapp/public/` is copied verbatim into the built bundle
and served on the public URL of every deployed instance — internal documentation has no
business being there.


These SVGs use the Quiet Nest product palette. The symbol geometry is identical across every file.

## Gradient assets

- `symbol-light.svg`: transparent, for light surfaces at 40px and larger
- `symbol-light-bg.svg`: light rounded tile
- `symbol-dark.svg`: transparent, for dark surfaces at 40px and larger
- `symbol-dark-bg.svg`: dark rounded tile

## Flat assets

The matching files in `flat/` are single-colour variants for 16–32px UI and favicon use.

- Light mark: `#235C55`
- Dark mark: `#81C9BD`

All files are self-contained, preserve `role="img"` and `aria-labelledby`, and contain no raster images, external fonts, linked resources, or filters.
