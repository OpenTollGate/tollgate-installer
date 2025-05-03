import React from 'react';
import styled from 'styled-components';
import Card from './common/Card';
import Button from './common/Button';

interface RouterInfo {
  ip: string;
  boardName?: string;
  architecture?: string;
  compatible?: boolean;
}

interface UpdateInfo {
  latest: string;
  installed: string;
  canUpgrade: boolean;
  nip94Event?: any;
}

interface CompleteProps {
  router: RouterInfo | null;
  updateInfo: UpdateInfo | null;
  onFlashNext: () => void;
}

const CompleteContainer = styled.div`
  padding: 1rem 0;
`;

const SuccessIcon = styled.div`
  width: 80px;
  height: 80px;
  background-color: ${props => props.theme.colors.success};
  border-radius: 50%;
  color: white;
  font-size: 40px;
  display: flex;
  align-items: center;
  justify-content: center;
  margin: 0 auto 2rem;
`;

const Title = styled.h2`
  text-align: center;
  font-size: ${props => props.theme.fontSizes['2xl']};
  color: ${props => props.theme.colors.text};
  margin-bottom: 1rem;
`;

const Subtitle = styled.p`
  text-align: center;
  color: ${props => props.theme.colors.textSecondary};
  margin-bottom: 2rem;
`;

const InfoCard = styled.div`
  background-color: ${props => props.theme.colors.backgroundAlt};
  border-radius: ${props => props.theme.radii.md};
  padding: 1.5rem;
  margin-bottom: 1.5rem;
`;

const InfoTitle = styled.h3`
  font-size: ${props => props.theme.fontSizes.lg};
  color: ${props => props.theme.colors.text};
  margin-bottom: 1rem;
`;

const InfoItem = styled.div`
  display: flex;
  justify-content: space-between;
  margin-bottom: 0.75rem;
  
  &:last-child {
    margin-bottom: 0;
  }
`;

const InfoLabel = styled.span`
  font-weight: ${props => props.theme.fontWeights.medium};
`;

const InfoValue = styled.span`
  color: ${props => props.theme.colors.textSecondary};
`;

const UpdateStatus = styled.div<{ hasUpdate: boolean }>`
  display: flex;
  align-items: center;
  padding: 0.75rem 1rem;
  border-radius: ${props => props.theme.radii.md};
  margin-bottom: 1.5rem;
  background-color: ${props => 
    props.hasUpdate 
      ? `${props.theme.colors.primaryLight}`
      : `${props.theme.colors.backgroundAlt}`
  };
  color: ${props => 
    props.hasUpdate 
      ? `${props.theme.colors.primary}`
      : `${props.theme.colors.textSecondary}`
  };
`;

const UpdateStatusIcon = styled.span`
  margin-right: 0.5rem;
  font-weight: bold;
`;

const ActionButton = styled(Button)`
  width: 100%;
  margin-top: 1.5rem;
`;

const FlashNextContainer = styled.div`
  text-align: center;
  padding: 1.5rem;
  border: 1px dashed ${props => props.theme.colors.border};
  border-radius: ${props => props.theme.radii.md};
  margin-top: 2rem;
`;

const FlashNextTitle = styled.h3`
  font-size: ${props => props.theme.fontSizes.lg};
  color: ${props => props.theme.colors.text};
  margin-bottom: 0.5rem;
`;

const FlashNextDescription = styled.p`
  color: ${props => props.theme.colors.textSecondary};
  margin-bottom: 1.5rem;
`;

const Complete: React.FC<CompleteProps> = ({
  router,
  updateInfo,
  onFlashNext
}) => {
  const hasUpdate = updateInfo?.canUpgrade || false;
  
  return (
    <Card title="Installation Complete">
      <CompleteContainer>
        <SuccessIcon>✓</SuccessIcon>
        
        <Title>TollGateOS Installed Successfully!</Title>
        <Subtitle>
          Your router is now running TollGateOS. It's ready to use.
        </Subtitle>
        
        {router && (
          <InfoCard>
            <InfoTitle>Router Information</InfoTitle>
            <InfoItem>
              <InfoLabel>Router IP:</InfoLabel>
              <InfoValue>{router.ip}</InfoValue>
            </InfoItem>
            {router.boardName && (
              <InfoItem>
                <InfoLabel>Model:</InfoLabel>
                <InfoValue>{router.boardName}</InfoValue>
              </InfoItem>
            )}
            {router.architecture && (
              <InfoItem>
                <InfoLabel>Architecture:</InfoLabel>
                <InfoValue>{router.architecture}</InfoValue>
              </InfoItem>
            )}
          </InfoCard>
        )}
        
        {updateInfo && (
          <>
            <InfoCard>
              <InfoTitle>TollGateOS Version</InfoTitle>
              <InfoItem>
                <InfoLabel>Installed Version:</InfoLabel>
                <InfoValue>{updateInfo.installed}</InfoValue>
              </InfoItem>
              <InfoItem>
                <InfoLabel>Latest Version:</InfoLabel>
                <InfoValue>{updateInfo.latest}</InfoValue>
              </InfoItem>
            </InfoCard>
            
            <UpdateStatus hasUpdate={hasUpdate}>
              <UpdateStatusIcon>
                {hasUpdate ? '!' : '✓'}
              </UpdateStatusIcon>
              {hasUpdate 
                ? 'A new version of TollGateOS is available!' 
                : 'Your router is running the latest version of TollGateOS'}
            </UpdateStatus>
          </>
        )}
        
        <FlashNextContainer>
          <FlashNextTitle>Want to flash another router?</FlashNextTitle>
          <FlashNextDescription>
            You can quickly install TollGateOS on another router by clicking the button below.
          </FlashNextDescription>
          
          <ActionButton 
            variant="primary" 
            size="large" 
            onClick={onFlashNext}
          >
            Flash Another Router
          </ActionButton>
        </FlashNextContainer>
      </CompleteContainer>
    </Card>
  );
};

export default Complete;