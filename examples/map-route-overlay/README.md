# CK3 route HTML/SVG overlay

This example keeps presentation out of the PNG renderer. It consumes the JSON sidecar returned by `map_render`, uses `route_points_output` without recalculating crop or padding, and draws the route in SVG over a fixed `2200×1300` PNG.

1. Call `map_route`, then pass its complete result to `map_render` with `auto_context=true`, `width=2200`, and `height=1300`.
2. Save the PNG as `route.png` and the metadata as `route-render.json` in this directory. The sidecar already contains endpoint names when the full route object was supplied.
3. Serve the directory because browsers normally block `fetch` from `file:` URLs:

```powershell
python -m http.server 8000
```

Open `http://127.0.0.1:8000/index.html?data=route-render.json&map=route.png`.

For a headless Chromium screenshot:

```powershell
chromium --headless --disable-gpu --hide-scrollbars --window-size=2200,1300 --screenshot=route-card.png "http://127.0.0.1:8000/index.html?data=route-render.json&map=route.png"
```

The renderer remains responsible for the `source_map`, `source_viewport`, `output`, `transform`, and `route_points_output` contract. The browser is only the presentation layer; ck3-index does not install or manage it.
