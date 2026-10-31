package ai.alertd.imagethat.drive

import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.contentOrNull
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive

/**
 * Mirrors `drive.File` (core/drive/client.go), as `mobile.Drive.list` returns it. Go sends [size]
 * as a string (Drive's own int64 encoding); [bytes] is the number.
 */
@Serializable
data class DriveFile(
    val id: String,
    val name: String,
    val mimeType: String = "",
    val size: String = "0",
    val modifiedTime: String = "",
    val thumbnailLink: String? = null,
    val isFolder: Boolean = false,
) {
    val bytes: Long get() = size.toLongOrNull() ?: 0L
}

/** Mirrors `drive.VaultRoot`: the vault a folder is in; [path] is empty when the folder is the root. */
@Serializable
data class DriveVaultRoot(val rootId: String, val path: List<String> = emptyList())

/** The file that makes a folder a gocryptfs vault. */
const val VAULT_CONFIG = "gocryptfs.conf"

// Go encodes a nil slice as null: coerce it to the default.
private val json = Json { ignoreUnknownKeys = true; coerceInputValues = true }

/** A folder listing (`mobile.Drive.list`): folders first, then by name. */
fun parseDriveFiles(s: String): List<DriveFile> =
    json.decodeFromString<List<DriveFile>>(s).sortedWith(compareByDescending<DriveFile> { it.isFolder }.thenBy { it.name.lowercase() })

/** `mobile.Drive.findVaultRoot`: "" when the folder is not in a vault. */
fun parseVaultRoot(s: String): DriveVaultRoot? = if (s.isBlank()) null else json.decodeFromString<DriveVaultRoot>(s)

/** Whether a listed folder is itself a vault (it holds gocryptfs.conf). */
fun isVaultFolder(files: List<DriveFile>): Boolean = files.any { !it.isFolder && it.name == VAULT_CONFIG }

/**
 * The client ID and secret from the JSON Google Cloud Console downloads for an OAuth client,
 * whose values sit under "installed" (Desktop app) or "web"; null when either is missing.
 */
fun parseOAuthClientJson(s: String): Pair<String, String>? {
    val root = runCatching { json.parseToJsonElement(s).jsonObject }.getOrNull() ?: return null
    val obj = (root["installed"] as? JsonObject) ?: (root["web"] as? JsonObject) ?: root
    val id = (obj["client_id"])?.let { runCatching { it.jsonPrimitive.contentOrNull }.getOrNull() }
    val secret = (obj["client_secret"])?.let { runCatching { it.jsonPrimitive.contentOrNull }.getOrNull() }
    if (id.isNullOrBlank() || secret.isNullOrBlank()) return null
    return id to secret
}
