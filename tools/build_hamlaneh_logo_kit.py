from __future__ import annotations

import sys
from pathlib import Path

import numpy as np
from PIL import Image, ImageFilter


PRIMARY_CROP = (80, 80, 584, 584)
SYMBOL_CROP = (204, 95, 464, 355)
FULL_SIZE = (4096, 4096)
SVG_VIEWBOX = 260


def rdp(points: list[tuple[float, float]], epsilon: float) -> list[tuple[float, float]]:
    if len(points) <= 2:
        return points

    start = np.array(points[0], dtype=np.float64)
    end = np.array(points[-1], dtype=np.float64)
    segment = end - start
    segment_length = float(np.linalg.norm(segment))

    if segment_length == 0:
        distances = [float(np.linalg.norm(np.array(point) - start)) for point in points]
    else:
        distances = []
        for point in points:
            relative = np.array(point) - start
            cross = segment[0] * relative[1] - segment[1] * relative[0]
            distances.append(abs(float(cross)) / segment_length)

    index = int(np.argmax(distances))
    maximum = distances[index]
    if maximum <= epsilon:
        return [points[0], points[-1]]

    left = rdp(points[: index + 1], epsilon)
    right = rdp(points[index:], epsilon)
    return left[:-1] + right


def simplify_closed_loop(
    points: list[tuple[float, float]], epsilon: float = 0.70
) -> list[tuple[float, float]]:
    if len(points) < 4:
        return [(float(x), float(y)) for x, y in points]

    ring = points[:-1] if points[0] == points[-1] else points[:]
    anchor = np.array(ring[0], dtype=np.float64)
    farthest = int(
        np.argmax([float(np.linalg.norm(np.array(point) - anchor)) for point in ring])
    )
    first = [(float(x), float(y)) for x, y in ring[: farthest + 1]]
    second_ring = ring[farthest:] + [ring[0]]
    second = [(float(x), float(y)) for x, y in second_ring]
    simplified = rdp(first, epsilon)[:-1] + rdp(second, epsilon)[:-1]
    return simplified


def loop_to_bezier(points: list[tuple[float, float]]) -> str:
    """Convert a closed contour to a smooth, corner-aware cubic Bézier path."""
    vertices = np.asarray(points, dtype=np.float64)
    count = len(vertices)
    if count < 3:
        return ""

    bounds = np.ptp(vertices, axis=0)
    small_round_detail = float(max(bounds)) <= 20.0
    tangents = np.zeros_like(vertices)

    for index in range(count):
        previous = vertices[(index - 1) % count]
        current = vertices[index]
        following = vertices[(index + 1) % count]
        incoming = previous - current
        outgoing = following - current
        incoming_length = float(np.linalg.norm(incoming))
        outgoing_length = float(np.linalg.norm(outgoing))
        if incoming_length < 1e-6 or outgoing_length < 1e-6:
            continue

        cosine = float(
            np.clip(
                np.dot(incoming, outgoing) / (incoming_length * outgoing_length),
                -1.0,
                1.0,
            )
        )
        interior_angle = float(np.degrees(np.arccos(cosine)))
        if small_round_detail:
            strength = 1.0
        elif interior_angle <= 75.0:
            strength = 0.04
        elif interior_angle <= 105.0:
            strength = 0.20
        elif interior_angle <= 130.0:
            strength = 0.55
        elif interior_angle <= 150.0:
            strength = 0.84
        else:
            strength = 1.0

        direction = following - previous
        direction_length = float(np.linalg.norm(direction))
        if direction_length < 1e-6:
            continue
        handle_length = min(incoming_length, outgoing_length) * 0.35 * strength
        tangents[index] = direction / direction_length * handle_length

    first_x, first_y = vertices[0]
    commands = [f"M{first_x:.2f},{first_y:.2f}"]
    for index in range(count):
        following_index = (index + 1) % count
        first_control = vertices[index] + tangents[index]
        second_control = vertices[following_index] - tangents[following_index]
        destination = vertices[following_index]
        commands.append(
            "C"
            f"{first_control[0]:.2f},{first_control[1]:.2f} "
            f"{second_control[0]:.2f},{second_control[1]:.2f} "
            f"{destination[0]:.2f},{destination[1]:.2f}"
        )
    commands.append("Z")
    return " ".join(commands)


def marching_squares(field: np.ndarray, level: float = 127.5) -> list[list[tuple[float, float]]]:
    padded = np.pad(field.astype(np.float64), 1, mode="constant")
    height, width = padded.shape

    def interpolate(a: float, b: float) -> float:
        if abs(b - a) < 1e-9:
            return 0.5
        return float(np.clip((level - a) / (b - a), 0.0, 1.0))

    segments: list[tuple[tuple[float, float], tuple[float, float]]] = []
    for y in range(height - 1):
        for x in range(width - 1):
            top_left = padded[y, x]
            top_right = padded[y, x + 1]
            bottom_right = padded[y + 1, x + 1]
            bottom_left = padded[y + 1, x]
            case = (
                (1 if top_left >= level else 0)
                | (2 if top_right >= level else 0)
                | (4 if bottom_right >= level else 0)
                | (8 if bottom_left >= level else 0)
            )
            if case in (0, 15):
                continue

            edge_points = {
                0: (x + interpolate(top_left, top_right), y),
                1: (x + 1, y + interpolate(top_right, bottom_right)),
                2: (x + interpolate(bottom_left, bottom_right), y + 1),
                3: (x, y + interpolate(top_left, bottom_left)),
            }
            simple_cases = {
                1: [(3, 0)],
                2: [(0, 1)],
                3: [(3, 1)],
                4: [(1, 2)],
                6: [(0, 2)],
                7: [(3, 2)],
                8: [(2, 3)],
                9: [(0, 2)],
                11: [(1, 2)],
                12: [(3, 1)],
                13: [(0, 1)],
                14: [(3, 0)],
            }
            if case in simple_cases:
                pairs = simple_cases[case]
            else:
                center = (top_left + top_right + bottom_right + bottom_left) / 4.0
                if case == 5:
                    pairs = [(0, 1), (2, 3)] if center >= level else [(3, 0), (1, 2)]
                else:  # case 10
                    pairs = [(3, 0), (1, 2)] if center >= level else [(0, 1), (2, 3)]

            for first, second in pairs:
                a = (edge_points[first][0] - 1.0, edge_points[first][1] - 1.0)
                b = (edge_points[second][0] - 1.0, edge_points[second][1] - 1.0)
                segments.append((a, b))

    def key(point: tuple[float, float]) -> tuple[float, float]:
        return (round(point[0], 4), round(point[1], 4))

    coordinates: dict[tuple[float, float], tuple[float, float]] = {}
    neighbours: dict[tuple[float, float], list[tuple[float, float]]] = {}
    for first, second in segments:
        first_key = key(first)
        second_key = key(second)
        coordinates[first_key] = first
        coordinates[second_key] = second
        neighbours.setdefault(first_key, []).append(second_key)
        neighbours.setdefault(second_key, []).append(first_key)

    def edge_key(
        first: tuple[float, float], second: tuple[float, float]
    ) -> tuple[tuple[float, float], tuple[float, float]]:
        return tuple(sorted((first, second)))  # type: ignore[return-value]

    visited: set[tuple[tuple[float, float], tuple[float, float]]] = set()
    loops: list[list[tuple[float, float]]] = []
    for start, options in neighbours.items():
        for first_step in options:
            initial_edge = edge_key(start, first_step)
            if initial_edge in visited:
                continue
            loop_keys = [start]
            previous = start
            current = first_step
            visited.add(initial_edge)

            for _ in range(len(segments) + 4):
                loop_keys.append(current)
                if current == start:
                    break
                candidates = [
                    item
                    for item in neighbours.get(current, [])
                    if item != previous and edge_key(current, item) not in visited
                ]
                if not candidates:
                    break
                following = candidates[0]
                visited.add(edge_key(current, following))
                previous, current = current, following

            if len(loop_keys) >= 5 and loop_keys[-1] == start:
                loop = [coordinates[item] for item in loop_keys]
                simplified = simplify_closed_loop(loop)
                if len(simplified) >= 3:
                    loops.append(simplified)
    return loops


def mask_to_loops(mask: np.ndarray) -> list[list[tuple[float, float]]]:
    height, width = mask.shape
    outgoing: dict[tuple[int, int], list[tuple[int, int]]] = {}

    def add_edge(start: tuple[int, int], end: tuple[int, int]) -> None:
        outgoing.setdefault(start, []).append(end)

    for y, x in np.argwhere(mask):
        y = int(y)
        x = int(x)
        if y == 0 or not mask[y - 1, x]:
            add_edge((x, y), (x + 1, y))
        if x == width - 1 or not mask[y, x + 1]:
            add_edge((x + 1, y), (x + 1, y + 1))
        if y == height - 1 or not mask[y + 1, x]:
            add_edge((x + 1, y + 1), (x, y + 1))
        if x == 0 or not mask[y, x - 1]:
            add_edge((x, y + 1), (x, y))

    edge_count = sum(len(values) for values in outgoing.values())
    loops: list[list[tuple[float, float]]] = []

    while edge_count:
        start = next(point for point, values in outgoing.items() if values)
        current = start
        loop: list[tuple[int, int]] = [start]
        guard = 0

        while guard <= edge_count + 4:
            guard += 1
            candidates = outgoing.get(current, [])
            if not candidates:
                break
            next_point = candidates.pop()
            edge_count -= 1
            current = next_point
            loop.append(current)
            if current == start:
                break

        if len(loop) >= 5 and loop[-1] == start:
            simplified = simplify_closed_loop(loop)
            if len(simplified) >= 3:
                loops.append(simplified)

    return loops


def create_symbol_path(source: Image.Image) -> str:
    crop = np.asarray(source.crop(SYMBOL_CROP).convert("RGB"), dtype=np.int16)
    distance = np.maximum(254 - crop, 0).max(axis=2)
    mask = distance >= 90

    # Remove isolated export-noise pixels without changing the logo geometry.
    padded = np.pad(mask.astype(np.uint8), 1)
    neighbours = np.zeros_like(mask, dtype=np.uint8)
    for dy in range(3):
        for dx in range(3):
            neighbours += padded[dy : dy + mask.shape[0], dx : dx + mask.shape[1]]
    mask &= neighbours >= 3

    softened = Image.fromarray(mask.astype(np.uint8) * 255, "L").filter(
        ImageFilter.GaussianBlur(radius=1.75)
    )
    loops = marching_squares(np.asarray(softened, dtype=np.float32))
    return " ".join(filter(None, (loop_to_bezier(loop) for loop in loops)))


def symbol_svg(path_data: str, mode: str, background: bool) -> str:
    if mode not in {"light", "dark"}:
        raise ValueError(mode)

    background_markup = ""
    if background:
        if mode == "light":
            background_markup = '<rect width="260" height="260" rx="28" fill="#FFFFFF"/>'
        else:
            background_markup = (
                '<rect width="260" height="260" rx="28" fill="url(#dark-bg)"/>'
            )

    fill = "url(#brand-gradient)" if mode == "light" else "#FFFFFF"
    return f'''<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 260 260" role="img" aria-labelledby="title desc">
  <title id="title">Hamlaneh symbol — {mode} theme</title>
  <desc id="desc">Interwoven communication loops surrounding a speech bubble with three dots.</desc>
  <defs>
    <linearGradient id="brand-gradient" x1="22" y1="20" x2="240" y2="242" gradientUnits="userSpaceOnUse">
      <stop offset="0" stop-color="#4F46E5"/>
      <stop offset="0.52" stop-color="#3B82F6"/>
      <stop offset="1" stop-color="#14B8A6"/>
    </linearGradient>
    <linearGradient id="dark-bg" x1="0" y1="0" x2="260" y2="260" gradientUnits="userSpaceOnUse">
      <stop offset="0" stop-color="#0F172A"/>
      <stop offset="1" stop-color="#06152B"/>
    </linearGradient>
  </defs>
  {background_markup}
  <path d="{path_data}" fill="{fill}" fill-rule="evenodd"/>
</svg>
'''


def hard_matte(image: Image.Image, threshold: float = 120.0) -> Image.Image:
    rgb = np.asarray(image.convert("RGB"), dtype=np.float32)
    distance = np.maximum(254.0 - rgb, 0.0).max(axis=2)
    foreground = distance >= threshold
    output_rgb = np.where(foreground[..., None], rgb, 0.0)
    alpha = foreground.astype(np.uint8) * 255
    rgba = np.dstack((output_rgb, alpha))
    return Image.fromarray(np.clip(rgba, 0, 255).astype(np.uint8), "RGBA")


def resize_rgba(image: Image.Image, size: tuple[int, int]) -> Image.Image:
    rgba = np.asarray(image.convert("RGBA"), dtype=np.float32)
    alpha = rgba[..., 3:4] / 255.0
    premultiplied = rgba[..., :3] * alpha

    color = Image.fromarray(np.clip(premultiplied, 0, 255).astype(np.uint8), "RGB")
    matte = Image.fromarray(np.clip(alpha[..., 0] * 255, 0, 255).astype(np.uint8), "L")
    color = color.resize(size, Image.Resampling.LANCZOS)
    matte = matte.resize(size, Image.Resampling.LANCZOS)

    resized_color = np.asarray(color, dtype=np.float32)
    resized_alpha = np.asarray(matte, dtype=np.float32) / 255.0
    resized_alpha[resized_alpha < 0.02] = 0.0
    straight = np.divide(
        resized_color,
        resized_alpha[..., None],
        out=np.zeros_like(resized_color),
        where=resized_alpha[..., None] > 1e-6,
    )
    result = np.dstack((straight, resized_alpha[..., None] * 255.0))
    return Image.fromarray(np.clip(result, 0, 255).astype(np.uint8), "RGBA")


def background(size: tuple[int, int], mode: str) -> Image.Image:
    width, height = size
    if mode == "light":
        return Image.new("RGB", size, "#FFFFFF")

    start = np.array([15, 23, 42], dtype=np.float32)
    end = np.array([6, 21, 43], dtype=np.float32)
    x = np.linspace(0.0, 1.0, width, dtype=np.float32)[None, :, None]
    y = np.linspace(0.0, 1.0, height, dtype=np.float32)[:, None, None]
    blend = (x + y) / 2.0
    pixels = start * (1.0 - blend) + end * blend
    pixels = np.broadcast_to(pixels, (height, width, 3))
    return Image.fromarray(np.clip(pixels, 0, 255).astype(np.uint8), "RGB")


def save_png(image: Image.Image, path: Path) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    image.save(path, "PNG", optimize=True, dpi=(300, 300))


def write_full_png_variants(source: Image.Image, output: Path) -> None:
    crop = source.crop(PRIMARY_CROP)
    light_transparent = resize_rgba(hard_matte(crop), FULL_SIZE)

    white_pixels = np.asarray(light_transparent, dtype=np.uint8).copy()
    white_pixels[..., :3] = 255
    dark_transparent = Image.fromarray(white_pixels, "RGBA")

    light_background = background(FULL_SIZE, "light")
    light_background.paste(light_transparent, (0, 0), light_transparent)
    dark_background = background(FULL_SIZE, "dark")
    dark_background.paste(dark_transparent, (0, 0), dark_transparent)

    full = output / "png" / "full-lockup"
    save_png(light_transparent, full / "hamlaneh-full-light-transparent-4096.png")
    save_png(light_background, full / "hamlaneh-full-light-background-4096.png")
    save_png(dark_transparent, full / "hamlaneh-full-dark-transparent-4096.png")
    save_png(dark_background, full / "hamlaneh-full-dark-background-4096.png")


def main() -> None:
    if len(sys.argv) != 3:
        raise SystemExit("usage: build_hamlaneh_logo_kit.py SOURCE_PNG OUTPUT_DIR")

    source_path = Path(sys.argv[1])
    output = Path(sys.argv[2])
    source = Image.open(source_path).convert("RGB")

    path_data = create_symbol_path(source)
    svg_dir = output / "svg" / "symbol"
    svg_dir.mkdir(parents=True, exist_ok=True)
    variants = [
        ("light", False, "hamlaneh-symbol-light-transparent.svg"),
        ("light", True, "hamlaneh-symbol-light-background.svg"),
        ("dark", False, "hamlaneh-symbol-dark-transparent.svg"),
        ("dark", True, "hamlaneh-symbol-dark-background.svg"),
    ]
    for mode, has_background, name in variants:
        (svg_dir / name).write_text(
            symbol_svg(path_data, mode, has_background), encoding="utf-8"
        )

    write_full_png_variants(source, output)
    print(f"Vector path length: {len(path_data):,} characters")
    print(f"SVG variants: {len(variants)}")
    print("Full-lockup PNG variants: 4")


if __name__ == "__main__":
    main()
