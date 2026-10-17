# Заглушка scripts/aitr.ps1 для тестов куратора: функции не исполняются,
# по ним только строится schema/aitr.md.
#
# PowerShell 5.1: без `&&`, `||`, тернарников и `??`.

$script:AitrRoot = Split-Path -Parent $PSScriptRoot
$script:AitrStatuses = @('planned', 'in-progress', 'done', 'blocked')
$script:AitrStacks = @('go', 'python', 'node', 'prompt')

# ---------------------------------------------------------------- утилиты ---

function Get-AitrPaths {
    $root = $script:AitrRoot
    return [pscustomobject]@{ Root = $root }
}

function Get-AitrAssignments {
    # Задания по assignments/*/assignment.json.
    return ,@()
}

# ----------------------------------------------------------------- sync ----

function Invoke-AitrSync {
    param([string[]]$Tokens)
    Write-Host 'sync'
}

# Проверка среды задания: метаданные, git, тулчейн, ключи, документация.
function Invoke-AitrDoctor {
    param([string[]]$Tokens)
    Write-Host 'doctor'
}

function Invoke-AitrList { param([string[]]$Tokens) Write-Host 'list' }

# ----------------------------------------------------------- диспетчер -----

function aitr {
    $cmd = [string]$args[0]
    switch ($cmd.ToLowerInvariant()) {
        'list'   { Invoke-AitrList   -Tokens $rest }
        'doctor' { Invoke-AitrDoctor -Tokens $rest }
        'sync'   { Invoke-AitrSync   -Tokens $rest }
        default  { Write-Host 'неизвестная команда' }
    }
}
