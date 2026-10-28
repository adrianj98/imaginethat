import com.android.build.api.artifact.SingleArtifact

plugins {
    alias(libs.plugins.android.application)
    alias(libs.plugins.kotlin.android)
    alias(libs.plugins.kotlin.compose)
    alias(libs.plugins.kotlin.serialization)
}

android {
    namespace = "ai.alertd.imagethat"
    compileSdk = 35

    defaultConfig {
        applicationId = "ai.alertd.imagethat"
        minSdk = 29
        targetSdk = 35
        versionCode = 1
        versionName = "0.1.0"
        testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"
        ndk { abiFilters += listOf("arm64-v8a", "x86_64") }
    }

    buildFeatures { compose = true }

    // The AES-SIV fixture vault is shared with the Go tests; SafStorageTest opens it.
    sourceSets["androidTest"].assets.srcDir(rootProject.projectDir.parentFile.resolve("webserver/vault/testdata"))

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlin { compilerOptions { jvmTarget.set(org.jetbrains.kotlin.gradle.dsl.JvmTarget.JVM_17) } }
}

dependencies {
    implementation(files("libs/vaultcore.aar"))
    implementation(libs.androidx.core.ktx)
    implementation(libs.androidx.activity.compose)
    implementation(platform(libs.compose.bom))
    implementation(libs.compose.ui)
    implementation(libs.compose.material3)
    implementation(libs.coil.compose)
    implementation(libs.androidx.biometric)
    implementation(libs.androidx.fragment)
    implementation(libs.kotlinx.serialization.json)
    implementation(libs.kotlinx.coroutines.android)
    implementation(libs.okhttp)

    testImplementation(libs.junit)
    testImplementation(libs.okhttp.mockwebserver)
    testImplementation(libs.okhttp.tls)
    testImplementation(libs.kotlinx.coroutines.test)

    androidTestImplementation(libs.androidx.test.ext.junit)
    androidTestImplementation(libs.androidx.test.runner)
    androidTestImplementation(libs.kotlinx.coroutines.android)
}

// Go vault engine -> libs/vaultcore.aar. Inputs are the Go sources so the
// (slow) gomobile bind only reruns when they change.
val buildVaultcore by tasks.registering(Exec::class) {
    val repoRoot = rootProject.projectDir.parentFile
    inputs.files(fileTree(File(repoRoot, "webserver")) {
        include("**/*.go", "go.mod", "go.sum", "scripts/sync-gocryptfs.sh")
        exclude("web/node_modules/**", "bin/**")
    })
    inputs.file(File(rootProject.projectDir, "vaultcore/build.sh"))
    outputs.file(layout.projectDirectory.file("libs/vaultcore.aar"))
    commandLine(File(rootProject.projectDir, "vaultcore/build.sh").absolutePath)
}
tasks.named("preBuild") { dependsOn(buildVaultcore) }

// ManifestHardeningTest reads the merged manifest; wire it as a task input so
// the merge always runs first.
androidComponents {
    onVariants(selector().withBuildType("debug")) { variant ->
        val mergedManifest = variant.artifacts.get(SingleArtifact.MERGED_MANIFEST)
        tasks.matching { it.name == "testDebugUnitTest" }.configureEach {
            inputs.file(mergedManifest)
            (this as Test).doFirst {
                systemProperty("merged.manifest", mergedManifest.get().asFile.absolutePath)
            }
        }
    }
}
