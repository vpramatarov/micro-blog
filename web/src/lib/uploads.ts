const BASE = "/uploads"

export type ImageSize = "s" | "m" | "l"

export function uploadsUrl(relPath?: string|null, size?: ImageSize): string {
    if (!relPath) {
        return ""
    }

    const rel = relPath.replace(/^\/+/, "")
    return `${BASE}/${size ? withSizeSuffix(rel, size) : rel}`
}

function withSizeSuffix(path: string, size: ImageSize): string {
    const separator = path.lastIndexOf(".")
    if (separator > path.lastIndexOf("/")) {
        return `${path.slice(0, separator)}-${size}${path.slice(separator)}`
    }

    return `${path}-${size}}`
}