package ai.alertd.imagethat.drive

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class DriveModelsTest {
    @Test fun listingParsesGoShapeFoldersFirst() {
        // core/drive.File: size is a JSON string, thumbnailLink is omitted when empty.
        val files = parseDriveFiles(
            """[{"id":"f1","name":"b.jpg","mimeType":"image/jpeg","size":"1234","modifiedTime":"2026-01-02T03:04:05Z","thumbnailLink":"https://t","isFolder":false},""" +
                """{"id":"d2","name":"zeta","mimeType":"application/vnd.google-apps.folder","size":"0","modifiedTime":"","isFolder":true},""" +
                """{"id":"d1","name":"Alpha","mimeType":"application/vnd.google-apps.folder","size":"0","modifiedTime":"","isFolder":true}]""",
        )
        assertEquals(listOf("Alpha", "zeta", "b.jpg"), files.map { it.name })
        assertEquals(1234L, files[2].bytes)
        assertEquals("https://t", files[2].thumbnailLink)
        assertNull(files[0].thumbnailLink)
        assertEquals(emptyList<DriveFile>(), parseDriveFiles("[]"))
    }

    @Test fun vaultFolderHoldsGocryptfsConf() {
        val conf = DriveFile("c", VAULT_CONFIG)
        assertTrue(isVaultFolder(listOf(DriveFile("d", "x", isFolder = true), conf)))
        assertFalse(isVaultFolder(listOf(DriveFile("d", VAULT_CONFIG, isFolder = true))))
        assertFalse(isVaultFolder(emptyList()))
    }

    @Test fun vaultRoot() {
        assertNull(parseVaultRoot(""))
        assertEquals(DriveVaultRoot("r1", listOf("a", "b")), parseVaultRoot("""{"rootId":"r1","path":["a","b"]}"""))
        assertEquals(DriveVaultRoot("r1"), parseVaultRoot("""{"rootId":"r1","path":null}"""))
    }

    @Test fun oauthClientJsonAsGoogleDownloadsIt() {
        assertEquals("id" to "sec", parseOAuthClientJson("""{"installed":{"client_id":"id","client_secret":"sec","redirect_uris":["http://localhost"]}}"""))
        assertEquals("id" to "sec", parseOAuthClientJson("""{"web":{"client_id":"id","client_secret":"sec"}}"""))
        assertEquals("id" to "sec", parseOAuthClientJson("""{"client_id":"id","client_secret":"sec"}"""))
        assertNull(parseOAuthClientJson("""{"installed":{"client_id":"id"}}"""))
        assertNull(parseOAuthClientJson("not json"))
    }
}
