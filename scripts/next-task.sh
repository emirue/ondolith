#!/bin/sh
# Answers "what do I work on next, and what proves it done?" from docs/81-work-breakdown.md.
#
# The plan already exists there: 123 tasks with prerequisites. What was missing
# was a way to *run* it — reading a 400-line document to find the next unblocked
# task is how a loop stalls. This prints the ready tasks and the exit signals
# that close them.
#
# There is no state file. A task is done when its D81 row says so, so the
# document is both the plan and the progress record; a second store would drift
# from it the way every other hand-kept copy in this repo has (M9, M11, M12).
#
#   scripts/next-task.sh          다음 작업 하나
#   scripts/next-task.sh --all    지금 착수 가능한 작업 전부
#   scripts/next-task.sh --status Phase 별 진행 수
#
# 종료 코드 — 루프 구동기가 이것으로 다음 행동을 정한다.
#
#   0  다음 작업을 출력했다      → 계속 돈다
#   2  남은 작업이 없다          → 루프를 정상 종료한다
#   3  남았는데 착수 가능한 게 없다 → 교착. 선행 관계를 고쳐야 한다
#   1  입력이 이상하다            → 문서가 깨졌다
#
# 셋을 전부 0으로 두면 자동 루프가 "끝났다"와 "막혔다"를 구분하지 못하고
# 조용히 성공한 것처럼 보인다 — .ai/MISTAKES.md M3 과 같은 계열이다.
set -u

cd "$(dirname "$0")/.." || exit 1
WBS=docs/81-work-breakdown.md
[ -f "$WBS" ] || { echo "작업 분해 문서가 없다: $WBS" >&2; exit 1; }

mode=${1:-next}

perl -e '
my ($wbs, $mode) = @ARGV;
open my $fh, "<", $wbs or do { print STDERR "$wbs: $!\n"; exit 1 };

my (@order, %task);
while (my $l = <$fh>) {
    # ID | 작업 | 선행 | 산출물 | 완료 기준
    next unless $l =~ /^\|\s*(W\d-\d+)\s*\|([^|]*)\|([^|]*)\|([^|]*)\|(.*)\|\s*$/;
    my ($id, $what, $pre, $out, $crit) = ($1, $2, $3, $4, $5);
    for ($what, $pre, $out, $crit) { s/^\s+//; s/\s+$// }
    next if exists $task{$id};
    push @order, $id;
    $task{$id} = {
        what => $what,
        # A task is complete when its own row records it, with evidence.
        done => ($what =~ /\*\*\(완료/ ? 1 : 0),
        opt  => ($what =~ /\(선택\)/ ? 1 : 0),
        pre  => [ $pre =~ /(W\d-\d+)/g ],
        out  => $out,
        crit => $crit,
    };
}
close $fh;
unless (@order) { print STDERR "$wbs 에서 작업을 하나도 읽지 못했다 (표 형식 확인)\n"; exit 1 }

if ($mode eq "--status") {
    my (%tot, %done);
    for my $id (@order) {
        my ($p) = $id =~ /^(W\d)/;
        $tot{$p}++;
        $done{$p}++ if $task{$id}{done};
    }
    printf "%-6s %s\n", "Phase", "완료 / 전체";
    for my $p (sort keys %tot) {
        printf "%-6s %4d / %d\n", $p, $done{$p} // 0, $tot{$p};
    }
    my $d = 0; $d += $_ for values %done;
    printf "%-6s %4d / %d\n", "합계", $d, scalar @order;
    exit 0;
}

# Ready = not done, and every prerequisite that exists in the document is done.
# Prerequisites naming something outside the table (e.g. "Phase 1 릴리즈") are
# left to the human — the script says so rather than pretending to know.
my @ready;
for my $id (@order) {
    next if $task{$id}{done};
    my @blocked = grep { exists $task{$_} && !$task{$_}{done} } @{ $task{$id}{pre} };
    push @ready, $id unless @blocked;
}

unless (@ready) {
    my $left = grep { !$task{$_}{done} } @order;
    if ($left) {
        print "착수 가능한 작업이 없다 — 남은 $left 건이 서로를 기다린다 (선행 순환).\n";
        exit 3;
    }
    print "남은 작업이 없다.\n";
    exit 2;
}

my @show = $mode eq "--all" ? @ready : ($ready[0]);
for my $id (@show) {
    my $t = $task{$id};
    print "\n" if $id ne $show[0];
    print "$id  $t->{what}\n";
    print "  산출물     $t->{out}\n" if $t->{out} ne "";
    print "  완료 기준   $t->{crit}\n" if $t->{crit} ne "";
    my @p = grep { exists $task{$_} } @{ $t->{pre} };
    print "  선행       ", join(", ", @p), " (전부 완료)\n" if @p;
    my @ext = grep { !exists $task{$_} } @{ $t->{pre} };
    print "  선행(문서 밖) ", join(", ", @ext), "\n" if @ext;
}

printf "\n착수 가능 %d건 / 남은 %d건\n",
    scalar @ready, scalar grep { !$task{$_}{done} } @order;
print "종료 신호는 docs/82-execution-loop.md 2절 — 공통 게이트 + 위 완료 기준\n";
' "$WBS" "$mode"
